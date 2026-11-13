// Package controlapi is the control service's controller API (contract
// C2) as the reconciler uses it: the desired feed with its cursor and
// long poll, and the status report. Every request carries the
// operator's token for the control service and the manager's GRN.
package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

const (
	// maxAnswer bounds a feed answer: 500 agents is the contract's
	// ceiling, and a body past this is not one.
	maxAnswer = 16 << 20
	// maxRefusal bounds a refusal's body.
	maxRefusal = 64 << 10
	// requestMargin is how long past the wait a request may take.
	requestMargin = 30 * time.Second
	// MaxWait is the longest wait the contract allows.
	MaxWait = 30 * time.Second
)

// ControllerHeader names the manager's GRN beside its token.
const ControllerHeader = "Controller-Grn"

// Options are what a Client is built from.
type Options struct {
	// URL is the control service's base URL, https.
	URL string
	// HTTP trusts the control service's root and speaks TLS 1.3.
	HTTP *http.Client
	// Tokens are the operator's tokens for the control service.
	Tokens Tokens
	// Controller is the manager's operator GRN, which is the client id
	// of the exchange.
	Controller string
	// Wait is the long poll's wait, at most MaxWait.
	Wait time.Duration
}

// Client implements agent.Feed.
type Client struct {
	opts Options

	mu      sync.Mutex
	cursor  string
	pending string
}

var _ agent.Feed = (*Client)(nil)

// New returns the Client over o.
func New(o Options) (*Client, error) {
	if o.Wait < 0 || o.Wait > MaxWait {
		return nil, errorx.New(errorx.KindInvalidArgument, "feed wait is outside 0 to 30 seconds")
	}
	return &Client{opts: o}, nil
}

// Desired asks for the whole releasable set after the settled cursor,
// waiting at the server for it to move. An answer it cannot read whole
// is an error: an empty set it returns is the feed's own `agents: []`.
func (c *Client) Desired(ctx context.Context) ([]agent.Desired, error) {
	c.mu.Lock()
	after := c.cursor
	c.mu.Unlock()

	query := url.Values{"waitSeconds": {strconv.Itoa(int(c.opts.Wait / time.Second))}}
	if after != "" {
		query.Set("after", after)
	}
	status, body, err := c.send(ctx, http.MethodGet, "/v1/operators/self/desired", query, nil, maxAnswer, c.opts.Wait)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, refusal("read the desired feed", status, body)
	}
	read, err := parseAnswer(body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pending = read.cursor
	c.mu.Unlock()
	return read.agents, nil
}

// Settle moves the cursor to the position of the last answer. The
// caller settles after it applied that answer, so a pass that failed
// asks again at once instead of waiting for a change it has not
// handled.
func (c *Client) Settle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = c.pending
}

type wireStatus struct {
	ObservedRevision string `json:"observedRevision"`
	RenderedRevision string `json:"renderedRevision"`
}

// ReportRendered reports that revision of g was observed and rendered.
func (c *Client) ReportRendered(ctx context.Context, g grn.GRN, revision string) error {
	body, err := json.Marshal(wireStatus{ObservedRevision: revision, RenderedRevision: revision})
	if err != nil {
		return errorx.Wrap(err, "render the status report", errorx.KindInternal)
	}
	path := "/v1/operators/self/agents/" + url.PathEscape(g.String()) + "/status"
	status, data, err := c.send(ctx, http.MethodPost, path, nil, body, maxRefusal, 0)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return refusal("report the rendered revision", status, data)
	}
	return nil
}

// wirePlacement is the body of a placement registration, as
// garamsh/garam-agent-operator docs/architecture/control.md pins it.
// `previous` is always null: this operator registers an agent's first
// placement only (ADR 0154).
type wirePlacement struct {
	Epoch       string  `json:"epoch"`
	PodUID      string  `json:"podUid"`
	PVCUID      string  `json:"pvcUid"`
	TokenSHA256 string  `json:"tokenSha256"`
	Previous    *string `json:"previous"`
}

// The 409 kinds that refuse a placement for good (contract C2).
const (
	kindPlacementSuperseded = "placement_superseded"
	kindEpochSuperseded     = "epoch_superseded"
)

// RegisterPlacement presents p as the placement of g. It answers true
// for a placement the control service stored (201) and false for one it
// already held (200). A 409 placement_superseded or epoch_superseded is
// agent.ErrPlacementSuperseded, any other 4xx is
// agent.ErrPlacementRefused; anything else is returned as it is and is
// worth presenting again.
func (c *Client) RegisterPlacement(ctx context.Context, g grn.GRN, p agent.Placement) (bool, error) {
	body, err := json.Marshal(wirePlacement{
		Epoch: p.Epoch, PodUID: p.PodUID, PVCUID: p.PVCUID, TokenSHA256: p.TokenSHA256,
	})
	if err != nil {
		return false, errorx.Wrap(err, "render the placement", errorx.KindInternal)
	}
	path := "/v1/operators/self/agents/" + url.PathEscape(g.String()) + "/placements"
	status, data, err := c.send(ctx, http.MethodPost, path, nil, body, maxRefusal, 0)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusCreated:
		return true, nil
	case status == http.StatusOK:
		return false, nil
	case status < http.StatusBadRequest || status >= http.StatusInternalServerError:
		return false, refusal("register the placement", status, data)
	}
	var wire struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(data, &wire)
	cause := refusal("register the placement", status, data)
	if status == http.StatusConflict && (wire.Kind == kindPlacementSuperseded || wire.Kind == kindEpochSuperseded) {
		return false, errorx.Wrap(agent.ErrPlacementSuperseded, cause.Error())
	}
	return false, errorx.Wrap(agent.ErrPlacementRefused, cause.Error())
}

// send makes one request, and a second under a fresh token where the
// control service answers 401. It returns the status and the whole
// body, or an error where the body could not be read whole.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, limit int64, wait time.Duration) (int, []byte, error) {
	status, data, err := c.once(ctx, method, path, query, body, limit, wait)
	if err == nil && status == http.StatusUnauthorized {
		c.opts.Tokens.Invalidate()
		return c.once(ctx, method, path, query, body, limit, wait)
	}
	return status, data, err
}

func (c *Client) once(ctx context.Context, method, path string, query url.Values, body []byte, limit int64, wait time.Duration) (int, []byte, error) {
	token, err := c.opts.Tokens.Token(ctx)
	if err != nil {
		return 0, nil, errorx.Wrap(err, "token for the control service")
	}
	ctx, cancel := context.WithTimeout(ctx, wait+requestMargin)
	defer cancel()
	target := c.opts.URL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errorx.Wrap(err, "build the control request", errorx.KindInternal)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(ControllerHeader, c.opts.Controller)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.opts.HTTP.Do(req)
	if err != nil {
		return 0, nil, errorx.Wrap(err, "control request", errorx.KindUnavailable)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return 0, nil, errorx.Wrap(err, "read the control answer", errorx.KindUnavailable)
	}
	if int64(len(data)) > limit {
		return 0, nil, errorx.New(errorx.KindInternal, "control answer is longer than "+strconv.FormatInt(limit, 10)+" bytes")
	}
	return res.StatusCode, data, nil
}

// refusal turns a status that is not the contract's success into an
// error. A refusal's body is read for its kind and message where it is
// one.
func refusal(op string, status int, body []byte) error {
	var wire struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	detail := ""
	if json.Unmarshal(body, &wire) == nil && wire.Kind != "" {
		detail = " (" + wire.Kind + "): " + wire.Message
	}
	msg := op + ": control service answered " + strconv.Itoa(status) + detail
	switch {
	case status == http.StatusUnprocessableEntity && wire.Kind == "too_many_agents":
		return errorx.New(errorx.KindResourceExhausted, msg)
	case status == http.StatusUnauthorized:
		return errorx.New(errorx.KindUnauthenticated, msg)
	case status == http.StatusForbidden:
		return errorx.New(errorx.KindPermissionDenied, msg)
	case status >= http.StatusInternalServerError:
		return errorx.New(errorx.KindUnavailable, msg)
	case status >= http.StatusBadRequest:
		return errorx.New(errorx.KindFailedPrecondition, msg)
	default:
		return errorx.New(errorx.KindInternal, msg)
	}
}
