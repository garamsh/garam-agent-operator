package introspector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const introspectionPath = "/operation-authorities/introspection"

// Garam introspects authorities on garam's machine listener.
type Garam struct {
	machine *garammachine.Client
}

var _ console.Introspector = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
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
	// RequestTarget is present for agent:cutover (garam@1a5273d) and for the reads and the
	// publication (garam@33b1c41), absent where the operation binds none.
	RequestTarget string     `json:"requestTarget"`
	ExpiresAt     *time.Time `json:"expiresAt"`
}

type assignmentJSON struct {
	OperatorGRN string `json:"operatorGrn"`
	Epoch       string `json:"epoch"`
}

// Introspect reads what authority binds. Introspection consumes nothing, so garam's undecided
// answers are retried by the machine client within its bound.
func (g *Garam) Introspect(ctx context.Context, authority console.Authority) (console.Binding, error) {
	answer, err := g.machine.Post(ctx, garammachine.OperationAuthority, introspectionPath, map[string]string{"authority": string(authority)})
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return console.Binding{}, fmt.Errorf("%w: %v", console.ErrAuthorityUndecided, err)
	case errors.As(err, &foreign):
		return console.Binding{}, fmt.Errorf("%w: %v", console.ErrGaramContractUnsupported, err)
	}
	if err != nil {
		return console.Binding{}, fmt.Errorf("introspect: %v", err)
	}
	switch answer.Status {
	case http.StatusNotFound:
		return console.Binding{}, console.ErrAuthorityUnknown
	case http.StatusForbidden:
		return console.Binding{}, console.ErrAuthorityForbidden
	case http.StatusOK:
	default:
		return console.Binding{}, fmt.Errorf("introspection answered %d: %s", answer.Status, garammachine.FirstLine(answer.Body))
	}
	var b bindingJSON
	if err := json.Unmarshal(answer.Body, &b); err != nil {
		return console.Binding{}, fmt.Errorf("decode introspection: %v", err)
	}
	out := console.Binding{
		OperationRef:  b.OperationRef,
		GrantID:       b.GrantID,
		Org:           b.OrgGRN,
		Actor:         b.ActorGRN,
		Audience:      b.AudienceGRN,
		Operation:     b.Operation,
		Target:        b.TargetGRN,
		RequestID:     b.RequestID,
		RequestTarget: b.RequestTarget,
		BodySHA256:    b.BodySHA256,
	}
	if b.ExpiresAt != nil {
		out.ExpiresAt = *b.ExpiresAt
	}
	if b.Assignment != nil {
		out.Assignment = &console.Assignment{Operator: b.Assignment.OperatorGRN, Epoch: b.Assignment.Epoch}
	}
	return out, nil
}
