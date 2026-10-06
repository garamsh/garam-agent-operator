package garammachine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// ExecutionFence is the contract of garam's activation and execution introspection.
	ExecutionFence = "execution-fence.v1"
	// AgentCutover is the contract of a legacy agent's cutover stages (garam@1a5273d, ADR-0086).
	AgentCutover = "agent-cutover.v1"

	// maxAnswerBytes bounds an answer read from garam.
	maxAnswerBytes = 1 << 20
)

// ErrUndecided is returned when garam answered 500 or 503, or could not be reached, on every attempt.
var ErrUndecided = errors.New("garam answered without deciding")

// ContractError is a decided answer garam gave under another contract version than the one
// asked, or under none. Its body is not read, so it decides nothing.
type ContractError struct {
	// Path is the route called, Status the status garam answered with.
	Path   string
	Status int
	// Got is the contract the answer carried, empty for none; Want is the one asked.
	Got, Want string
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("%s answered %d under contract %q, want %q", e.Path, e.Status, e.Got, e.Want)
}

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

// Target is the request target a call to path is sent with: path under any path the listener's
// base URL carries, as garam receives it and as a reference bound to that route must name it.
func (c *Client) Target(path string) string {
	if base, err := url.Parse(c.baseURL); err == nil {
		return strings.TrimSuffix(base.EscapedPath(), "/") + path
	}
	return path
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
// decided answer under another contract version, or none, is refused with a *ContractError.
func (c *Client) Post(ctx context.Context, contract, path string, body any) (Answer, error) {
	return c.Send(ctx, Call{Method: http.MethodPost, Contract: contract, Path: path, Body: body})
}

// Call is one request to garam's machine listener. Body is sent as JSON, and nothing where it is
// nil; Raw, where set, is sent in its place as exactly these bytes, for a route whose authority
// binds the digest of the body garam receives. Authorization, where set, is sent as that header.
type Call struct {
	Method        string
	Contract      string
	Path          string
	Body          any
	Raw           []byte
	Authorization string
}

// Send sends call and returns garam's decided answer, retried as Post is.
func (c *Client) Send(ctx context.Context, call Call) (Answer, error) {
	payload := call.Raw
	if payload == nil && call.Body != nil {
		encoded, err := json.Marshal(call.Body)
		if err != nil {
			return Answer{}, fmt.Errorf("encode %s: %v", call.Path, err)
		}
		payload = encoded
	}
	wait := c.Backoff
	for attempt := 1; ; attempt++ {
		answer, retry, err := c.sendOnce(ctx, call, payload)
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

// sendOnce sends one attempt, and reports whether garam left it undecided.
func (c *Client) sendOnce(ctx context.Context, call Call, payload []byte) (Answer, bool, error) {
	contract, path := call.Contract, call.Path
	var sent io.Reader
	if payload != nil {
		sent = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, call.Method, c.baseURL+path, sent)
	if err != nil {
		return Answer{}, false, fmt.Errorf("build %s: %v", path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(contractHeader, contract)
	if call.Authorization != "" {
		req.Header.Set("Authorization", call.Authorization)
	}
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
		return Answer{}, false, &ContractError{Path: path, Status: resp.StatusCode, Got: got, Want: contract}
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
