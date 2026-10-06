package desired

import (
	"context"
	"errors"
	"fmt"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// longPoll is how long one request waits for the feed to move. ADR 0040
	// allows at most 30 seconds.
	longPoll = 30 * time.Second

	// transientFirst and transientLast bound the wait before asking again after
	// a 500, a 503 or no answer: it starts short and doubles to the cap.
	transientFirst = time.Second
	transientLast  = 30 * time.Second

	// refusedWait is how long the puller waits after a 4xx before asking again.
	// A refusal is definite and is never asked again at once, but it can clear —
	// a renewed certificate, a grant reissued, agents moved away — so the
	// puller keeps asking, slowly.
	refusedWait = 5 * time.Minute
)

// Puller pulls the controller's desired set from the control service and
// renders each agent in it. It is a manager Runnable. The feed is
// level-triggered, so every answer is the whole set: an agent absent from one is
// withheld for now and left as it is, never deleted.
type Puller struct {
	client   *Client
	renderer Renderer

	// The waits, held here so a test can shorten them.
	longPoll, transientFirst, transientLast, refusedWait time.Duration

	// recoveries is told every answer's open recoveries, where it is set.
	recoveries interface{ Offer(map[string]OpenRecovery) }

	// rendered is what was last rendered and reported for each GRN, its
	// revision and whether a stop held it (renderedKey), so an unchanged answer
	// is not rendered again. It is held in memory only: after a restart every
	// agent is rendered once more, which writes nothing where its Agent already
	// matches.
	rendered map[string]string
}

// NewPuller returns a Puller reading through client and writing through
// renderer.
func NewPuller(client *Client, renderer Renderer) *Puller {
	return &Puller{
		client: client, renderer: renderer,
		longPoll: longPoll, transientFirst: transientFirst, transientLast: transientLast, refusedWait: refusedWait,
		rendered: map[string]string{},
	}
}

// Start pulls until ctx is cancelled. It returns no error, because an error from
// a Runnable stops the manager, and a control service that is unreachable or
// refusing is what the next request is for.
func (p *Puller) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("desired")
	log.Info("Pulling desired state from the control service")

	// The cursor is held in memory only: a request without one is answered at
	// once with the whole set, which is all a restart needs.
	cursor := ""
	backoff := p.transientFirst
	refusedAs := ""
	for ctx.Err() == nil {
		wait := p.longPoll
		if cursor == "" {
			wait = 0
		}
		answer, err := p.client.Desired(ctx, cursor, wait)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if refusal, refused := asRefusal(err); refused {
				feedRefused.Set(1)
				if kind := fmt.Sprintf("%d %s", refusal.Status, refusal.Kind); kind != refusedAs {
					log.Error(refusal, "The control service refused the desired feed; asking again slowly",
						"status", refusal.Status, "kind", refusal.Kind, "wait", p.refusedWait)
					refusedAs = kind
				}
				pause(ctx, p.refusedWait)

				continue
			}
			log.Info("The desired feed did not answer; asking again", "error", err.Error(), "wait", backoff)
			pause(ctx, backoff)
			backoff = min(2*backoff, p.transientLast)

			continue
		}

		feedRefused.Set(0)
		refusedAs = ""
		backoff = p.transientFirst
		p.offerRecoveries(answer)
		for _, agent := range answer.Agents {
			p.apply(ctx, agent)
		}
		cursor = answer.Cursor
	}

	return nil
}

// OfferRecoveriesTo has every answer's open recoveries offered to r, which
// prepares them (ADR 0059).
func (p *Puller) OfferRecoveriesTo(r interface{ Offer(map[string]OpenRecovery) }) {
	p.recoveries = r
}

// offerRecoveries offers the open recoveries of one whole answer: one the
// answer does not name is no longer open.
func (p *Puller) offerRecoveries(answer Answer) {
	if p.recoveries == nil {
		return
	}
	open := map[string]OpenRecovery{}
	for _, agent := range answer.Agents {
		if agent.Recovery != nil {
			open[agent.GRN] = *agent.Recovery
		}
	}
	p.recoveries.Offer(open)
}

// apply renders one agent's revision and reports it, where it is not the
// revision last rendered for that agent.
func (p *Puller) apply(ctx context.Context, agent Agent) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", agent.GRN, "revision", agent.Revision)
	if p.rendered[agent.GRN] == renderedKey(agent) {
		return
	}

	err := p.renderer.Render(ctx, agent)
	switch {
	case errors.Is(err, ErrNotControlSource):
		log.Info("Not rendering an agent whose desired state another source holds")

		return
	case errors.Is(err, ErrMalformed):
		log.Error(err, "Not rendering a revision that cannot be rendered")

		return
	case err != nil:
		log.Error(err, "Failed to render a revision")

		return
	}

	if err := p.client.ReportStatus(ctx, agent.GRN, agent.Revision, agent.Revision); err != nil {
		log.Error(err, "Failed to report a rendered revision")

		return
	}
	p.rendered[agent.GRN] = renderedKey(agent)
	log.Info("Rendered a revision", "stopped", agent.Stopped)
}

// renderedKey is what an answer renders for an agent: its revision, and whether
// a stop holds it, which changes no revision (ADR 0057).
func renderedKey(agent Agent) string {
	if agent.Stopped {
		return agent.Revision + "/stopped"
	}

	return agent.Revision
}

// pause waits for d, or until ctx is cancelled.
func pause(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
