package machinetoken

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// contractHeader and assertionContract are the version every answer of the exchange carries.
	contractHeader    = "Garam-Contract-Version"
	assertionContract = "machine-token.v2"

	// firstBackoff is the wait after a first failed exchange, doubled after each further one up to
	// lastBackoff; a 429 waits its Retry-After instead.
	firstBackoff = time.Second
	lastBackoff  = 5 * time.Minute

	// maxAnswerBytes bounds an exchange's answer.
	maxAnswerBytes = 64 << 10
)

// Config is what a Source exchanges with.
type Config struct {
	// Endpoint is the URL the exchange is sent to: garam's /oauth/token on the listener this
	// process dials. It need not equal the tokenUrl the assertion names.
	Endpoint string
	// Files are the identity's three files.
	Files Files
	// Audience, where set, is the GRN of the operator the token is minted to be shown to; empty
	// mints garam's own.
	Audience string
	// HTTP verifies garam's server certificate and presents no client certificate.
	HTTP *http.Client
	// Now and Random default to the clock and crypto/rand.
	Now    func() time.Time
	Random io.Reader
	// Failed, where set, is told of each failed exchange.
	Failed func(*Error)
	// IdentityURL, where set, is garam's getIdentity on the listener HTTP verifies, which Keep
	// calls under the token (ADR 0075). It is set for a token garam itself accepts and no other.
	IdentityURL string
}

// Source holds one token at a time and exchanges a new assertion for the next: before the one
// held enters the last third of its lifetime, after a protected call refused it, and never more
// often than its backoff allows.
type Source struct {
	cfg Config

	mu       sync.Mutex
	token    string
	clientID string
	refresh  time.Time
	expiry   time.Time
	failures int
	until    time.Time
	failed   *Error

	// streak is what Check reads. It holds its own lock, so Check never waits on an exchange.
	streak streak
}

// NewSource returns a Source exchanging under cfg.
func NewSource(cfg Config) *Source {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Source{cfg: cfg}
}

// Token answers the token to present now. A failed exchange answers the token still held while it
// lives, and the failure once it does not.
func (s *Source) Token(ctx context.Context) (string, error) {
	token, _, err := s.credential(ctx)
	return token, err
}

// credential is Token, with the client_id the token was exchanged under.
func (s *Source) credential(ctx context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	if s.token != "" && now.Before(s.refresh) {
		return s.token, s.clientID, nil
	}
	if now.Before(s.until) {
		return s.held(now)
	}
	if err := s.exchange(ctx, now); err != nil {
		return s.held(now)
	}
	return s.token, s.clientID, nil
}

// Invalidate drops token where it is the one held, so the next Token exchanges again.
func (s *Source) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token = ""
	}
}

// held answers the token held while it lives, and the last failure otherwise.
func (s *Source) held(now time.Time) (string, string, error) {
	if s.token != "" && now.Before(s.expiry) {
		return s.token, s.clientID, nil
	}
	if s.failed != nil {
		return "", "", s.failed
	}
	return "", "", errors.New("no garam token is held")
}

// exchange sends one fresh assertion and keeps what garam answers, or records the failure and when
// the next exchange may be sent.
func (s *Source) exchange(ctx context.Context, now time.Time) error {
	token, clientID, lifetime, retryAfter, reason, err := s.send(ctx, now)
	if err != nil {
		s.failures++
		wait := retryAfter
		if wait <= 0 {
			wait = min(firstBackoff<<min(s.failures-1, 16), lastBackoff)
		}
		s.until = now.Add(wait)
		s.failed = &Error{Reason: reason, Until: s.until, Err: err}
		s.streak.fail(now, causeExchange, s.failed)
		if s.cfg.Failed != nil {
			s.cfg.Failed(s.failed)
		}
		return s.failed
	}
	s.token, s.clientID, s.expiry, s.refresh = token, clientID, now.Add(lifetime), now.Add(lifetime*2/3)
	s.failures, s.until, s.failed = 0, time.Time{}, nil
	s.streak.end(causeExchange)
	return nil
}

// oauthToken is RFC 6749's token answer; oauthError its error answer.
type (
	oauthToken struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	oauthError struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
)

// send performs one exchange, answering the token and the client_id it was exchanged under. A
// failure answers its reason, and the wait garam asked for where it asked for one.
func (s *Source) send(ctx context.Context, now time.Time) (string, string, time.Duration, time.Duration, Reason, error) {
	id, err := readIdentity(s.cfg.Files)
	if err != nil {
		return "", "", 0, 0, ReasonKey, err
	}
	assertion, err := sign(id, now, s.cfg.Random)
	if err != nil {
		return "", "", 0, 0, ReasonKey, err
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {id.clientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	if s.cfg.Audience != "" {
		form.Set("audience", s.cfg.Audience)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, 0, ReasonUnavailable, fmt.Errorf("build the exchange: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.cfg.HTTP.Do(request)
	if err != nil {
		return "", "", 0, 0, ReasonUnavailable, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return "", "", 0, 0, ReasonUnavailable, fmt.Errorf("read the exchange's answer: %w", err)
	}

	switch {
	case response.StatusCode == http.StatusOK:
		if got := response.Header.Get(contractHeader); got != assertionContract {
			return "", "", 0, 0, ReasonUnavailable, fmt.Errorf("garam answered the exchange under contract %q, want %q", got, assertionContract)
		}
		var answered oauthToken
		if err := json.Unmarshal(body, &answered); err != nil {
			return "", "", 0, 0, ReasonUnavailable, errors.New("garam's token answer is not RFC 6749 JSON")
		}
		if answered.AccessToken == "" || !strings.EqualFold(answered.TokenType, "Bearer") || answered.ExpiresIn <= 0 {
			return "", "", 0, 0, ReasonUnavailable, errors.New("garam's token answer carries no bearer token or no lifetime")
		}
		return answered.AccessToken, id.clientID, time.Duration(answered.ExpiresIn) * time.Second, 0, "", nil
	case response.StatusCode == http.StatusTooManyRequests:
		return "", "", 0, retryAfter(response.Header.Get("Retry-After")), ReasonThrottled,
			errors.New("garam answered 429: too many exchanges")
	case response.StatusCode >= http.StatusInternalServerError:
		return "", "", 0, 0, ReasonUnavailable, fmt.Errorf("garam answered %d", response.StatusCode)
	}
	var refused oauthError
	_ = json.Unmarshal(body, &refused)
	if refused.Error == "invalid_client" {
		return "", "", 0, 0, ReasonInvalidClient, ErrInvalidClient
	}
	return "", "", 0, 0, ReasonRefused, fmt.Errorf("garam answered %d %q: %s", response.StatusCode, refused.Error, refused.Description)
}

// retryAfter reads Retry-After as the integer seconds garam sends, and as no wait otherwise.
func retryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
