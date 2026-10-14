package garam

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// refreshPass is how often the refresher reads which agents' tokens are due.
	refreshPass = 10 * time.Second

	// refusedWait is how long an agent whose mint garam refused, or answered at
	// another epoch, waits before it is asked for again: a refusal is not a
	// refresh, and asking again at once changes nothing.
	refusedWait = 5 * time.Minute

	// failedWait is how long an agent whose mint or write failed for any other
	// reason waits before it is tried again.
	failedWait = 30 * time.Second
)

// The refresh window, as fractions of a token's lifetime from its mint: garam
// ADR-0100 (garam@afde8b8) has the assignee refresh at a point in it picked at
// random, so a fleet does not refresh at one moment, and leaves the last quarter
// of the lifetime for the new token to reach the file its adapter reads.
const (
	windowStart = 2.0 / 3.0
	windowEnd   = 3.0 / 4.0
)

// ErrMintEpochSuperseded is a token garam minted at another epoch than the one
// the agent's spec names. It is not written: the agent is started under its
// spec's epoch, and a token of another names an assignment the agent does not
// run under. Like a refusal, it waits rather than being asked for again as a
// refresh.
var ErrMintEpochSuperseded = errors.New("garam minted the token at another epoch than the agent's spec names")

// Refresher keeps the tokens of every agent this operator holds current in the
// file its adapter reads (ADR 0073). It mints an agent's tokens where none are
// placed, where a recovery was finalized, and at a random point in garam's
// refresh window, and never exchanges a token for its successor: each is
// minted by this operator's own token.
//
// It is a manager Runnable, in the leader-election group, so one process mints.
type Refresher struct {
	client *Client
	store  TokenStore
	// controlAudience is the control service's operator GRN, which a fenced
	// agent's control token is minted for; empty where no control service is
	// configured.
	controlAudience string

	// now and window are the clock and the point in the refresh window, held
	// here so a test can fix them.
	now    func() time.Time
	window func() float64

	// waiting is when each agent a mint failed for is tried again.
	waiting map[GRN]time.Time
}

// NewRefresher returns a Refresher minting through client and writing through
// store, minting a fenced agent's control token for controlAudience.
func NewRefresher(client *Client, store TokenStore, controlAudience string) *Refresher {
	return &Refresher{
		client: client, store: store, controlAudience: controlAudience,
		now:     time.Now,
		window:  func() float64 { return windowStart + rand.Float64()*(windowEnd-windowStart) },
		waiting: map[GRN]time.Time{},
	}
}

// Start refreshes until ctx is cancelled. It returns no error: an error from a
// Runnable stops the manager, and a garam that cannot be reached is what the
// next pass is for.
func (r *Refresher) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("garam").Info("Refreshing the tokens of the agents this operator holds",
		"interval", refreshPass)
	wait.UntilWithContext(ctx, r.Refresh, refreshPass)
	return nil
}

// Refresh mints and writes the tokens of every agent due, once.
func (r *Refresher) Refresh(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("garam")
	holders, err := r.store.Holders(ctx)
	if err != nil {
		log.Error(err, "Failed to list the agents whose tokens this operator keeps")
		return
	}
	listed := map[GRN]bool{}
	for _, holder := range holders {
		listed[holder.Agent] = true
		now := r.now()
		if now.Before(holder.Due) || now.Before(r.waiting[holder.Agent]) {
			continue
		}
		if err := r.refresh(ctx, holder); err != nil {
			wait := failedWait
			if errors.Is(err, ErrAgentNotHeld) || errors.Is(err, ErrMintEpochSuperseded) {
				wait = refusedWait
			}
			r.waiting[holder.Agent] = now.Add(wait)
			log.Error(err, "Failed to refresh an agent's tokens", "agent", holder.Agent, "wait", wait)
			continue
		}
		delete(r.waiting, holder.Agent)
	}
	for agent := range r.waiting {
		if !listed[agent] {
			delete(r.waiting, agent)
		}
	}
}

// refresh mints the holder's tokens and writes them, due again at a point in
// the refresh window of the one that expires first.
func (r *Refresher) refresh(ctx context.Context, holder TokenHolder) error {
	minted := r.now()
	tokens := Tokens{}
	var err error
	if tokens.Garam, err = r.mint(ctx, holder, ""); err != nil {
		return err
	}
	expires := tokens.Garam.ExpiresAt
	if holder.Fenced && r.controlAudience != "" {
		control, err := r.mint(ctx, holder, r.controlAudience)
		if err != nil {
			return err
		}
		tokens.Control = &control
		if control.ExpiresAt.Before(expires) {
			expires = control.ExpiresAt
		}
	}
	due := minted.Add(time.Duration(float64(expires.Sub(minted)) * r.window()))
	if err := r.store.Store(ctx, holder, tokens, due); err != nil {
		return fmt.Errorf("write the tokens of %s: %w", holder.Agent, err)
	}
	return nil
}

// mint asks garam for one of the holder's tokens, and refuses one minted at
// another epoch than the holder's spec names.
func (r *Refresher) mint(ctx context.Context, holder TokenHolder, audience string) (AgentToken, error) {
	token, err := r.client.MintAgentToken(ctx, holder.Agent, audience)
	countRefusal(runnableRefresher, err)
	if err != nil {
		return AgentToken{}, err
	}
	if holder.Epoch != 0 && token.Epoch != holder.Epoch {
		return AgentToken{}, fmt.Errorf("mint a token for %s: %w: minted at %d, the spec names %d",
			holder.Agent, ErrMintEpochSuperseded, token.Epoch, holder.Epoch)
	}
	return token, nil
}
