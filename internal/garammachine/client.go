package garammachine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// contractHeader names the version a route of garam's speaks; garam refuses any other
	// version with 400 (api/machine.yaml).
	contractHeader = "Garam-Contract-Version"

	// OperationAuthority is the contract of introspection and the controller proof (garam@f2ac780).
	OperationAuthority = "operation-authority.v1"
	// ManagedEnrollment is the contract of managed create (garam@7ca51b9).
	ManagedEnrollment = "managed-enrollment.v1"

	// maxAnswerBytes bounds an answer read from garam.
	maxAnswerBytes = 1 << 20
)

// ErrUndecided is returned when garam answered 500 or 503, or could not be reached, on every attempt.
var ErrUndecided = errors.New("garam answered without deciding")

// Client calls garam's machine listener as the operator its client certificate names.
type Client struct {
	baseURL string
	client  *http.Client
	// Attempts bounds how many times one call is sent while garam answers 500 or 503 or the
	// connection fails; Backoff is the wait before the second, doubled before each after.
	Attempts int
	Backoff  time.Duration
}

// New returns a Client calling the machine listener at baseURL through client, which carries
// this service's operator certificate.
func New(baseURL string, client *http.Client) *Client {
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), client: client, Attempts: 3, Backoff: 200 * time.Millisecond}
}

// Answer is a decided answer from garam: any status but 500 and 503.
type Answer struct {
	Status int
	Body   []byte
}

// Post sends body as JSON to path under contract and returns garam's decided answer. Every route
// it is used for either decides without writing or answers a repeated request identifier with
// the first request's outcome, so an attempt garam answered 500 or 503, or whose connection
// failed, is sent again up to Attempts times, as garam's ADR-0050 places a listener's 5xx. A
// decided answer under another contract version is refused.
func (c *Client) Post(ctx context.Context, contract, path string, body any) (Answer, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Answer{}, fmt.Errorf("encode %s: %v", path, err)
	}
	wait := c.Backoff
	for attempt := 1; ; attempt++ {
		answer, retry, err := c.postOnce(ctx, contract, path, payload)
		if !retry {
			return answer, err
		}
		if attempt >= c.Attempts {
			return Answer{}, fmt.Errorf("%w after %d attempts: %v", ErrUndecided, attempt, err)
		}
		select {
		case <-ctx.Done():
			return Answer{}, fmt.Errorf("%w: %v", ErrUndecided, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// postOnce sends one attempt, and reports whether garam left it undecided.
func (c *Client) postOnce(ctx context.Context, contract, path string, payload []byte) (Answer, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return Answer{}, false, fmt.Errorf("build %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(contractHeader, contract)
	resp, err := c.client.Do(req)
	if err != nil {
		return Answer{}, true, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return Answer{}, true, fmt.Errorf("read answer: %v", err)
	}
	if resp.StatusCode == http.StatusInternalServerError || resp.StatusCode == http.StatusServiceUnavailable {
		return Answer{}, true, fmt.Errorf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get(contractHeader); got != contract {
		return Answer{}, false, fmt.Errorf("%s answered %d under contract %q, want %q", path, resp.StatusCode, got, contract)
	}
	return Answer{Status: resp.StatusCode, Body: body}, false, nil
}

// FirstLine is the start of an answer's body, bounded so an error stays one line.
func FirstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
