package introspector

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

	"github.com/garamsh/garam-agent-operator/internal/console"
)

const (
	// contractHeader and contractVersion name the contract garam's operation-authority routes
	// speak (garam@f2ac780, api/machine.yaml); garam refuses any other version with 400.
	contractHeader  = "Garam-Contract-Version"
	contractVersion = "operation-authority.v1"

	introspectionPath = "/operation-authorities/introspection"
)

// Garam introspects authorities on garam's machine listener, as the operator its client
// certificate names.
type Garam struct {
	baseURL string
	client  *http.Client
	// Attempts bounds how many times one introspection is sent while garam answers 500 or 503
	// or the connection fails; Backoff is the wait before the second, doubled before each after.
	Attempts int
	Backoff  time.Duration
}

var _ console.Introspector = (*Garam)(nil)

// NewGaram returns a Garam calling the machine listener at baseURL through client, which
// carries this service's operator certificate.
func NewGaram(baseURL string, client *http.Client) *Garam {
	return &Garam{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		client:   client,
		Attempts: 3,
		Backoff:  200 * time.Millisecond,
	}
}

// bindingJSON is garam's OperationBinding schema; every property is camelCase.
type bindingJSON struct {
	OperationRef string          `json:"operationRef"`
	GrantID      string          `json:"grantId"`
	OrgGRN       string          `json:"orgGrn"`
	ActorGRN     string          `json:"actorGrn"`
	AudienceGRN  string          `json:"audienceGrn"`
	Operation    string          `json:"operation"`
	TargetGRN    string          `json:"targetGrn"`
	Assignment   *assignmentJSON `json:"assignment"`
	RequestID    string          `json:"requestId"`
	BodySHA256   string          `json:"bodySha256"`
	ExpiresAt    *time.Time      `json:"expiresAt"`
}

type assignmentJSON struct {
	OperatorGRN string `json:"operatorGrn"`
	Epoch       string `json:"epoch"`
}

// errRetryable is an answer garam may give differently to the same introspection sent again.
var errRetryable = errors.New("garam answered without deciding")

// Introspect reads what authority binds. Introspection consumes nothing, so an attempt garam
// answered 500 or 503, or whose connection failed, is sent again up to Attempts times.
func (g *Garam) Introspect(ctx context.Context, authority console.Authority) (console.Binding, error) {
	body, err := json.Marshal(map[string]string{"authority": string(authority)})
	if err != nil {
		return console.Binding{}, fmt.Errorf("encode introspection: %v", err)
	}
	wait := g.Backoff
	for attempt := 1; ; attempt++ {
		b, err := g.introspectOnce(ctx, body)
		if !errors.Is(err, errRetryable) {
			return b, err
		}
		if attempt >= g.Attempts {
			return console.Binding{}, fmt.Errorf("%w after %d attempts: %v", console.ErrAuthorityUndecided, attempt, err)
		}
		select {
		case <-ctx.Done():
			return console.Binding{}, fmt.Errorf("%w: %v", console.ErrAuthorityUndecided, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (g *Garam) introspectOnce(ctx context.Context, body []byte) (console.Binding, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+introspectionPath, bytes.NewReader(body))
	if err != nil {
		return console.Binding{}, fmt.Errorf("build introspection: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(contractHeader, contractVersion)
	resp, err := g.client.Do(req)
	if err != nil {
		return console.Binding{}, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return console.Binding{}, fmt.Errorf("%w: read answer: %v", errRetryable, err)
	}

	switch resp.StatusCode {
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		return console.Binding{}, fmt.Errorf("%w: status %d", errRetryable, resp.StatusCode)
	case http.StatusNotFound:
		return console.Binding{}, console.ErrAuthorityUnknown
	case http.StatusForbidden:
		return console.Binding{}, console.ErrAuthorityForbidden
	case http.StatusOK:
	default:
		return console.Binding{}, fmt.Errorf("introspection answered %d: %s", resp.StatusCode, firstLine(answer))
	}
	if got := resp.Header.Get(contractHeader); got != contractVersion {
		return console.Binding{}, fmt.Errorf("introspection answered contract %q, want %q", got, contractVersion)
	}
	var b bindingJSON
	if err := json.Unmarshal(answer, &b); err != nil {
		return console.Binding{}, fmt.Errorf("decode introspection: %v", err)
	}
	out := console.Binding{
		OperationRef: b.OperationRef,
		GrantID:      b.GrantID,
		Org:          b.OrgGRN,
		Actor:        b.ActorGRN,
		Audience:     b.AudienceGRN,
		Operation:    b.Operation,
		Target:       b.TargetGRN,
		RequestID:    b.RequestID,
		BodySHA256:   b.BodySHA256,
	}
	if b.ExpiresAt != nil {
		out.ExpiresAt = *b.ExpiresAt
	}
	if b.Assignment != nil {
		out.Assignment = &console.Assignment{Operator: b.Assignment.OperatorGRN, Epoch: b.Assignment.Epoch}
	}
	return out, nil
}

// firstLine is the start of an error answer, bounded so a log line stays one line.
func firstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
