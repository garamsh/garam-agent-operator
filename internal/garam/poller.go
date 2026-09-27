package garam

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Poller reads this operator's definitions from garam on an interval, claims
// the ones it has not claimed yet, and constructs an agent for each one it
// holds a claim on, keeping the image of the ones it already built current with
// this operator's configuration.
//
// It runs beside the reconcilers rather than inside one: what wakes a poll is
// the clock, not an event on an Agent, and controller-runtime's shape for that
// is a Runnable. Added to a manager it joins the leader-election group
// (sigs.k8s.io/controller-runtime@v0.24.1:pkg/manager/runnable_group.go:99), so
// leader election is what keeps two processes from racing for one claim.
type Poller struct {
	client      *Client
	constructor Constructor
	interval    time.Duration
}

// NewPoller returns a Poller reading client's definitions every interval and
// constructing what it holds through constructor.
func NewPoller(client *Client, constructor Constructor, interval time.Duration) *Poller {
	return &Poller{client: client, constructor: constructor, interval: interval}
}

// Start polls until ctx is cancelled, which is what makes a Poller a
// manager.Runnable. It returns no error: an error here stops the manager, and
// a garam that cannot be reached is what the next poll is for.
func (p *Poller) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("garam").Info("Polling garam for definitions", "interval", p.interval)
	wait.UntilWithContext(ctx, p.poll, p.interval)
	return nil
}

// poll reads the definitions once, claims each one garam does not already hold
// a claim for, and constructs each one this operator holds, correcting the image
// of one it built before.
func (p *Poller) poll(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("garam")

	definitions, err := p.client.ListDefinitions(ctx)
	if err != nil {
		log.Error(err, "Failed to read the definitions garam holds for this operator")
		return
	}
	log.V(1).Info("Read the definitions garam holds for this operator", "count", len(definitions))

	for _, definition := range definitions {
		claim := definition.Claim
		if claim == nil {
			assignment, held := p.claim(ctx, definition.Agent)
			if !held {
				continue
			}
			claim = &Claim{Epoch: assignment.Epoch}
		}
		if len(definition.Ignored) > 0 {
			// Per-pass detail rather than a state change: a definition carrying a
			// stray key reports it on every pass, because a poll has no memory.
			// The names travel and the strings beside them do not — a key with no
			// contract here is one nothing can act on.
			log.V(1).Info("Ignored the values of a definition this operator holds no contract for",
				"agent", definition.Agent, "keys", definition.Ignored)
		}
		p.correct(ctx, definition.Agent)
		p.construct(ctx, definition, claim.Epoch)
	}
}

// correct brings the image of an agent this operator already built to the one it
// is configured with, so that a corrected configuration reaches the agents
// constructed while it was wrong.
//
// It runs before the credential is looked at rather than beside the certificate,
// because construction is what places a credential and the agents this repairs
// have one already: gated on a missing credential it would repair nothing.
func (p *Poller) correct(ctx context.Context, agent GRN) {
	log := logf.FromContext(ctx).WithName("garam")

	corrected, err := p.constructor.CorrectImage(ctx, agent)
	if err != nil {
		log.Error(err, "Failed to correct the image of an agent this operator constructed", "agent", agent)
		return
	}
	if corrected {
		log.Info("Corrected the image of an agent this operator constructed", "agent", agent)
	}
}

// claim binds this operator to agent and returns the assignment it now holds,
// reporting whether it holds one at all.
func (p *Poller) claim(ctx context.Context, agent GRN) (Assignment, bool) {
	log := logf.FromContext(ctx).WithName("garam")

	assignment, err := p.client.ClaimDefinition(ctx, agent)
	switch {
	case errors.Is(err, ErrClaimConflict):
		// Terminal rather than retryable: a definition is claimed once, so
		// the claim this operator holds is not one it can take again.
		log.Error(err, "Refused a claim garam already holds", "agent", agent)
		return Assignment{}, false
	case err != nil:
		log.Error(err, "Failed to claim a definition", "agent", agent)
		return Assignment{}, false
	}
	log.Info("Claimed a definition", "agent", assignment.Agent, "epoch", assignment.Epoch)
	return assignment, true
}

// construct builds the agent a claim admits this operator to, where the cluster
// does not already carry its credential.
//
// The certificate is asked for only here, where what comes back can be stored
// at once: garam generates the private key per certificate and stores none, so
// a credential that reaches nothing is gone. What recovers one is another
// certificate rather than another write, which the next pass asks for.
//
// This is also the only place epoch is written, and it is written because the
// certificate route just proved it: that route answers for an agent assigned to
// this operator at the current epoch and refuses every other caller, so an
// answer here means the epoch in hand is the one this operator holds the agent
// at. Nowhere else can say that. A definition's claim reports the assignment's
// epoch whoever holds it, so an epoch refreshed from a later poll would read
// current however long ago this operator was replaced — and a report carrying
// it could never be found stale, which is what the epoch is on the report for.
func (p *Poller) construct(ctx context.Context, definition Definition, epoch int64) {
	log := logf.FromContext(ctx).WithName("garam")
	agent := definition.Agent

	placed, err := p.constructor.HasCredential(ctx, agent)
	if err != nil {
		log.Error(err, "Failed to read whether an agent's credential is placed", "agent", agent)
		return
	}
	if placed {
		return
	}

	credential, err := p.client.IssueAgentCertificate(ctx, agent)
	switch {
	case errors.Is(err, ErrAgentNotHeld):
		// garam answers the same for an agent held by another operator and for
		// one that does not exist, so there is nothing to tell apart and
		// nothing to retry against: the next read of the definitions is it.
		log.Error(err, "Constructed nothing: garam does not hold this agent for this operator", "agent", agent)
		return
	case err != nil:
		log.Error(err, "Failed to obtain an agent's credential", "agent", agent)
		return
	}

	if err := p.constructor.Construct(ctx, definition, epoch, credential); err != nil {
		log.Error(err, "Lost a certificate garam issued an agent: it exists nowhere else, and the next "+
			"pass asks for another rather than retrying this write", "agent", agent)
		return
	}
	log.Info("Constructed an agent", "agent", agent, "epoch", epoch, "notAfter", credential.NotAfter)
}
