package machinetoken

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SustainedFailure is how long every outcome a Source observed must have been a failure before
// Check reports it (ADR 0075). It is one lifetime of the token garam answers, expires_in 3600
// (garam@9ba35c3 api/machine.yaml exchangeClientAssertion): no token outlives it, so a failure
// this long is not one a held token is riding out, and the backoff, capped at five minutes, has
// tried again many times inside it.
const SustainedFailure = time.Hour

// keepInterval is how often Keep asks for a token.
const keepInterval = 30 * time.Second

// errCallRefused is a call presenting the source's token answered 401, after the one re-exchange
// the Transport sends.
var errCallRefused = errors.New("a call presenting this operator's token was answered 401")

// streak is the run of failures a Source has observed since its last success.
type streak struct {
	mu sync.Mutex
	// since is when the run began, and zero while the last outcome observed succeeded.
	since time.Time
	last  error
}

func (s *streak) succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.since, s.last = time.Time{}, nil
}

func (s *streak) failed(now time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.since.IsZero() {
		s.since = now
	}
	s.last = err
}

// Check answers an error while every exchange this source sent, and every call its token was
// presented on, has failed for longer than SustainedFailure, and nil otherwise. An exchange that
// answers a token, or a call answered anything but 401 or a 5xx, ends the run. It never waits on
// an exchange in flight, and what it answers names no key and no token.
func (s *Source) Check() error {
	s.streak.mu.Lock()
	since, last := s.streak.since, s.streak.last
	s.streak.mu.Unlock()
	if since.IsZero() || s.cfg.Now().Sub(since) <= SustainedFailure {
		return nil
	}
	return fmt.Errorf("every exchange and call under this operator's garam token has failed since %s: %w",
		since.UTC().Format(time.RFC3339), last)
}

// Keep asks for a token every keepInterval until ctx ends, so the source exchanges on its own
// schedule while nothing calls through it and Check reads a recovery that no call has met yet.
// A token held and not yet due is answered without contacting garam, and the backoff paces a
// failing one.
func (s *Source) Keep(ctx context.Context) {
	ticker := time.NewTicker(keepInterval)
	defer ticker.Stop()
	for {
		// A failure reaches Failed and the streak inside Token; the token itself is not wanted here.
		_, _ = s.Token(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// observe records what a call presenting the source's token was answered. A transport error and a
// 5xx say nothing of the token and are not recorded; a failure to obtain a token was recorded by
// the exchange that failed.
func (s *Source) observe(response *http.Response, err error) {
	switch {
	case err != nil || response.StatusCode >= http.StatusInternalServerError:
	case response.StatusCode == http.StatusUnauthorized:
		s.streak.failed(s.cfg.Now(), errCallRefused)
	default:
		s.streak.succeeded()
	}
}
