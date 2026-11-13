// Package loop drives the reconciler: one pass after another, paced by
// the feed's long poll, and a failed pass is retried after a bounded,
// growing backoff.
package loop

import (
	"context"
	"log/slog"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
)

// Options are what Run is given.
type Options struct {
	Service agent.Service
	// Settle is called after each pass that succeeded, so the feed's
	// cursor moves only past what was applied.
	Settle func()
	// Observe is told each pass's outcome.
	Observe func(error)
	// MinBackoff and MaxBackoff bound the wait after a failed pass. The
	// wait doubles from the first to the second, and a success resets it.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	Logger     *slog.Logger
}

// Run reconciles until ctx is done, then returns nil. A pass that fails
// is logged and retried; it never ends the loop.
func Run(ctx context.Context, o Options) error {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	backoff := o.MinBackoff
	for ctx.Err() == nil {
		err := o.Service.Reconcile(ctx)
		if ctx.Err() != nil {
			return nil
		}
		o.Observe(err)
		if err == nil {
			o.Settle()
			backoff = o.MinBackoff
			continue
		}
		log.Error("reconcile pass failed", slog.String("error", err.Error()), slog.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, o.MaxBackoff)
	}
	return nil
}
