package desired

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// recoverPass is how often the recoverer looks for recoveries to take a step.
	recoverPass = time.Second

	// preparedWait is how long a prepared recovery waits before it is asked
	// about again: until an administrator finalizes it, there is nothing new.
	preparedWait = 10 * time.Second
)

// RecoveryRequest is a recovery's request as it is persisted before it is
// sent: the recovery's request id on garam and the epoch it asks for. garam's
// recovery under execution-fence.v2 signs nothing, so it carries no key and no
// certificate request (garam@afde8b8).
type RecoveryRequest struct {
	ID    string
	Epoch string
}

// Recovered is the control service's answer to a recovery's request: the
// recovery it is for, and the agent's new lineage once the recovery is
// finalized, empty before that.
type Recovered struct {
	Agent     string
	RequestID string
	Epoch     string
	Lineage   string
}

// RecoveryStore holds what the recoverer persists and reads.
type RecoveryStore interface {
	// Recovering lists the managed agents with a recovery request persisted.
	Recovering(ctx context.Context) ([]string, error)

	// LoadRecovery returns the recovery request persisted for agent, and false
	// where none is.
	LoadRecovery(ctx context.Context, agent string) (RecoveryRequest, bool, error)

	// SaveRecovery persists request for agent. A request already persisted is
	// kept, never replaced: garam binds its request id to one body.
	SaveRecovery(ctx context.Context, agent string, request RecoveryRequest) error

	// Recovered records lineage on agent's credential and has its tokens minted
	// again, in the new lineage, and then removes the persisted recovery
	// request. An agent with no credential placed has nothing to record: its
	// first tokens are minted in the new lineage.
	Recovered(ctx context.Context, agent, lineage string) error
}

// Recoverer takes each managed agent's open credential recovery through this
// operator's half (#300, ADR 0073): a request persisted before it is sent, then
// the new lineage, recorded on the agent's credential so that its tokens are
// minted again in it and its Pod moves. It is a manager Runnable. Which
// recoveries are open it learns from the desired feed, through Offer; a
// persisted request is taken on whether the feed still names it, so a recovery
// finalized and no longer open is still collected.
type Recoverer struct {
	client *Client
	store  RecoveryStore

	// The waits, held here so a test in the package can shorten them.
	pass, transientFirst, transientLast, refusedWait, preparedWait time.Duration

	mu      sync.Mutex
	offered map[string]OpenRecovery

	attempts map[string]*attempt

	// recorded is the recovery each agent's lineage was last recorded from.
	// The feed may still name it until its next answer, and a new request under
	// its id would be refused as finalized under another. It is held in memory:
	// after a restart the feed's first answer no longer names it.
	recorded map[string]string
}

// attempt is what the recoverer remembers of an agent between passes.
type attempt struct {
	next    time.Time
	backoff time.Duration

	// refusedAs is the last refusal logged, so a refusal is logged once and
	// not on every attempt.
	refusedAs string
}

// NewRecoverer returns a Recoverer asking through client and persisting
// through store.
func NewRecoverer(client *Client, store RecoveryStore) *Recoverer {
	return &Recoverer{
		client: client, store: store,
		pass: recoverPass, transientFirst: transientFirst, transientLast: transientLast,
		refusedWait: refusedWait, preparedWait: preparedWait,
		offered: map[string]OpenRecovery{}, attempts: map[string]*attempt{}, recorded: map[string]string{},
	}
}

// Offer replaces the open recoveries the recoverer knows of with those of one
// whole answer of the feed.
func (r *Recoverer) Offer(open map[string]OpenRecovery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offered = open
}

// Start recovers until ctx is cancelled. It returns no error, because an error
// from a Runnable stops the manager.
func (r *Recoverer) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("desired").Info("Recovering the credentials of managed agents")
	for ctx.Err() == nil {
		r.recoverAll(ctx)
		pause(ctx, r.pass)
	}

	return nil
}

// recoverAll takes a step for every agent with a persisted request or an open
// recovery.
func (r *Recoverer) recoverAll(ctx context.Context) {
	persisted, err := r.store.Recovering(ctx)
	if err != nil {
		logf.FromContext(ctx).WithName("desired").Error(err, "Failed to list the managed agents being recovered")

		return
	}
	r.mu.Lock()
	offered := r.offered
	r.mu.Unlock()
	agents := map[string]bool{}
	for _, agent := range persisted {
		agents[agent] = true
	}
	for agent := range offered {
		agents[agent] = true
	}
	for agent := range agents {
		open, isOpen := offered[agent]
		var opened *OpenRecovery
		if isOpen {
			opened = &open
		}
		r.recover(ctx, agent, opened)
	}
}

// recover takes one agent one step through its recovery. A request persisted
// is finished before another is begun, whatever the feed now names, because
// garam binds its request id to one body.
func (r *Recoverer) recover(ctx context.Context, agent string, open *OpenRecovery) {
	log := logf.FromContext(ctx).WithName("desired").WithValues("agent", agent)
	state := r.attempts[agent]
	if state == nil {
		state = &attempt{}
		r.attempts[agent] = state
	}
	if time.Now().Before(state.next) {
		return
	}

	request, found, err := r.store.LoadRecovery(ctx, agent)
	if err != nil {
		log.Error(err, "Failed to read the persisted recovery request")
		r.retryTransient(state)

		return
	}
	if !found {
		if open == nil || r.recorded[agent] == open.RequestID {
			delete(r.attempts, agent)

			return
		}
		if request, err = r.begin(ctx, agent, *open); err != nil {
			log.Error(err, "Failed to persist the recovery request")
			r.retryTransient(state)

			return
		}
	}

	recovered, err := r.client.PrepareRecovery(ctx, agent, request)
	if refusal, refused := asRefusal(err); refused {
		state.backoff, state.next = 0, time.Now().Add(r.refusedWait)
		if kind := fmt.Sprintf("%d %s", refusal.Status, refusal.Kind); kind != state.refusedAs {
			log.Error(refusal, "The control service refused the recovery request; asking again slowly",
				"status", refusal.Status, "kind", refusal.Kind, "wait", r.refusedWait)
			state.refusedAs = kind
		}

		return
	}
	if err != nil {
		log.Info("The recovery request did not answer; asking again", "error", err.Error())
		r.retryTransient(state)

		return
	}
	if recovered.Agent != agent || recovered.RequestID != request.ID || recovered.Epoch != request.Epoch {
		log.Error(errors.New("the answer does not match the request"), "Not recording a recovery of another request",
			"answeredAgent", recovered.Agent, "answeredRequest", recovered.RequestID)
		state.next = time.Now().Add(r.refusedWait)

		return
	}
	if recovered.Lineage == "" {
		// Prepared: an administrator's finalize is what comes next.
		state.backoff, state.next = 0, time.Now().Add(r.preparedWait)

		return
	}

	if err := r.store.Recovered(ctx, agent, recovered.Lineage); err != nil {
		log.Error(err, "Failed to record the recovered lineage")
		r.retryTransient(state)

		return
	}
	delete(r.attempts, agent)
	r.recorded[agent] = request.ID
	log.Info("Recorded the recovered lineage of a managed agent; its tokens are minted again in it",
		"lineage", recovered.Lineage)
}

// begin persists the request for the open recovery, under its request id and
// epoch, and returns what is persisted.
func (r *Recoverer) begin(ctx context.Context, agent string, open OpenRecovery) (RecoveryRequest, error) {
	if err := r.store.SaveRecovery(ctx, agent, RecoveryRequest{ID: open.RequestID, Epoch: open.Epoch}); err != nil {
		return RecoveryRequest{}, err
	}
	// What is sent is what was persisted, whoever persisted it.
	persisted, found, err := r.store.LoadRecovery(ctx, agent)
	if err != nil {
		return RecoveryRequest{}, err
	}
	if !found {
		return RecoveryRequest{}, errors.New("the recovery request was not persisted")
	}

	return persisted, nil
}

// retryTransient schedules the next attempt after a wait doubling to the cap.
func (r *Recoverer) retryTransient(state *attempt) {
	if state.backoff == 0 {
		state.backoff = r.transientFirst
	} else {
		state.backoff = min(2*state.backoff, r.transientLast)
	}
	state.next = time.Now().Add(state.backoff)
}
