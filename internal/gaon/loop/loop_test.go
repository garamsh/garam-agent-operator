package loop_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/loop"
)

// service answers each Reconcile from a script, then cancels the loop.
type service struct {
	script []error
	n      int
	cancel context.CancelFunc
	at     []time.Time
}

func (s *service) Reconcile(context.Context) error {
	s.at = append(s.at, time.Now())
	if s.n >= len(s.script) {
		s.cancel()
		return nil
	}
	err := s.script[s.n]
	s.n++
	return err
}

func run(t *testing.T, script []error, min, max time.Duration) (svc *service, settled, observed *atomic.Int32, errs *[]error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc = &service{script: script, cancel: cancel}
	settled, observed = new(atomic.Int32), new(atomic.Int32)
	errs = new([]error)
	err := loop.Run(ctx, loop.Options{
		Service: svc, Settle: func() { settled.Add(1) },
		Observe:    func(err error) { observed.Add(1); *errs = append(*errs, err) },
		MinBackoff: min, MaxBackoff: max, Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	return svc, settled, observed, errs
}

func TestRunSettlesOnlyAfterASuccessfulPass(t *testing.T) {
	boom := errors.New("boom")
	_, settled, observed, errs := run(t, []error{nil, boom, nil}, time.Millisecond, 4*time.Millisecond)
	assert.Equal(t, int32(2), settled.Load(), "the failed pass is not settled")
	assert.Equal(t, int32(3), observed.Load())
	assert.Equal(t, []error{nil, boom, nil}, *errs)
}

func TestRunBacksOffByDoublingUpToTheBound(t *testing.T) {
	boom := errors.New("boom")
	svc, _, _, _ := run(t, []error{boom, boom, boom, boom, boom}, 20*time.Millisecond, 40*time.Millisecond)
	require.GreaterOrEqual(t, len(svc.at), 6)
	gap := func(i int) time.Duration { return svc.at[i+1].Sub(svc.at[i]) }
	assert.GreaterOrEqual(t, gap(0), 20*time.Millisecond)
	assert.GreaterOrEqual(t, gap(1), 40*time.Millisecond)
	// Bounded: the fourth wait is the bound, not 160ms.
	assert.Less(t, gap(4), 120*time.Millisecond)
}

func TestRunStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	svc := &blocking{}
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx, loop.Options{
			Service: svc, Settle: func() {}, Observe: func(error) {},
			MinBackoff: time.Hour, MaxBackoff: time.Hour,
		})
	}()
	cancel()
	assert.NoError(t, <-done)
}

type blocking struct{}

func (blocking) Reconcile(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRunStopsDuringABackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	svc := &service{script: []error{errors.New("x")}, cancel: func() {}}
	done := make(chan error)
	go func() {
		done <- loop.Run(ctx, loop.Options{
			Service: svc, Settle: func() {}, Observe: func(error) { cancel() },
			MinBackoff: time.Hour, MaxBackoff: time.Hour,
		})
	}()
	assert.NoError(t, <-done)
}
