package machinetoken

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// SustainedFailure is how long every outcome a Source observed must have been a failure before
// Check reports it (ADR 0075). It is one lifetime of the token garam answers, expires_in 3600
// (garam@9ba35c3 api/machine.yaml exchangeClientAssertion): no token outlives it, so a failure
// this long is not one a held token is riding out, and Keep brings a new outcome many times
// inside it.
const SustainedFailure = time.Hour

const (
	// keepInterval is how often Keep asks for a token.
	keepInterval = 30 * time.Second
	// identityInterval is how often Keep calls getIdentity, where it has a URL to call.
	identityInterval = 5 * time.Minute
)

// errCallRefused is a call presenting the source's token answered 401, after the one re-exchange
// the Transport sends.
var errCallRefused = errors.New("a call presenting this operator's token was answered 401")

// cause is what began a run of failures, which decides what ends it.
type cause int

const (
	// causeExchange is a failed exchange; the next exchange that answers a token ends it.
	causeExchange cause = iota + 1
	// causeCall is a call refused 401; only a call answered otherwise ends it, because the
	// Transport's re-exchange after a 401 answers a token whether or not garam accepts it.
	causeCall
)

// streak is the run of failures a Source has observed since its last success.
type streak struct {
	mu sync.Mutex
	// since is when the run began, and zero while no run is open.
	since time.Time
	cause cause
	last  error
}

func (s *streak) fail(now time.Time, c cause, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.since.IsZero() {
		s.since = now
	}
	s.cause = max(s.cause, c)
	s.last = err
}

func (s *streak) end(c cause) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cause <= c {
		s.since, s.cause, s.last = time.Time{}, 0, nil
	}
}

// Check answers an error while every exchange this source sent, and every call its token was
// presented on, has failed for longer than SustainedFailure, and nil otherwise. A call answered
// anything but 401 or a 5xx ends the run; an exchange that answers a token ends it only where no
// call was refused since it began. It never waits on an exchange in flight, and what it answers
// names no key and no token.
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
// schedule while nothing calls through it. Where the source names an IdentityURL it also calls it
// at once and every identityInterval through a Transport of its own, so a source nothing calls
// through still meets a call's outcome and Check reads a recovery no other call has met. A token
// held and not yet due is answered without contacting garam, and the backoff paces a failing one.
func (s *Source) Keep(ctx context.Context) {
	tokens := time.NewTicker(keepInterval)
	defer tokens.Stop()
	var identities <-chan time.Time
	if s.cfg.IdentityURL != "" {
		ticker := time.NewTicker(identityInterval)
		defer ticker.Stop()
		identities = ticker.C
		s.identify(ctx)
	}
	for {
		// A failure reaches Failed and the streak inside Token; the token itself is not wanted here.
		_, _ = s.Token(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tokens.C:
		case <-identities:
			s.identify(ctx)
		}
	}
}

// identify calls getIdentity under the source's token. Its outcome reaches the streak through
// the Transport, so nothing is read from the answer and no failure is returned here.
func (s *Source) identify(ctx context.Context) {
	base := s.cfg.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client := &http.Client{Timeout: s.cfg.HTTP.Timeout, Transport: &Transport{Source: s, Base: base}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.IdentityURL, nil)
	if err != nil {
		// An IdentityURL that does not parse shares its base with Endpoint, whose exchange fails
		// and is recorded the same way.
		return
	}
	response, err := client.Do(request)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

// observe records what a call presenting the source's token was answered. A transport error and a
// 5xx say nothing of the token and are not recorded; a failure to obtain a token was recorded by
// the exchange that failed.
func (s *Source) observe(response *http.Response, err error) {
	switch {
	case err != nil || response.StatusCode >= http.StatusInternalServerError:
	case response.StatusCode == http.StatusUnauthorized:
		s.streak.fail(s.cfg.Now(), causeCall, errCallRefused)
	default:
		s.streak.end(causeCall)
	}
}
