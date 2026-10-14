package prover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/garamsh/garam-agent-operator/internal/distribution"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam proves controllers on garam's machine listener, through introspectController under
// operation-authority.v2 (garam@9ba35c3, api/machine.yaml).
type Garam struct {
	machine *garammachine.Client
}

var _ distribution.Prover = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// introspectionJSON is garam's ControllerIntrospection schema under operation-authority.v2.
type introspectionJSON struct {
	Token string  `json:"token"`
	Agent *string `json:"agent"`
}

// proofJSON is garam's ControllerProof schema.
type proofJSON struct {
	Operator string `json:"operator"`
	Org      string `json:"org"`
	Agent    *struct {
		GRN   string `json:"grn"`
		Epoch string `json:"epoch"`
	} `json:"agent"`
}

// Prove forwards token exactly as the controller presented it. Every answer but 200 refuses; 403,
// 404 and 422 are garam's refusals — 422 for a token that is not live, names another controller or
// another audience — and 500 and 503 are retried by the machine client within its bound.
func (g *Garam) Prove(ctx context.Context, controller, token, agent string) (distribution.Proof, error) {
	in := introspectionJSON{Token: token}
	if agent != "" {
		in.Agent = &agent
	}
	answer, err := g.machine.Post(ctx, garammachine.OperationAuthority, "/operators/"+url.PathEscape(controller)+"/introspection", in)
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return distribution.Proof{}, fmt.Errorf("%w: %v", distribution.ErrUndecided, err)
	case errors.As(err, &foreign):
		return distribution.Proof{}, fmt.Errorf("%w: %v", distribution.ErrGaramContractUnsupported, err)
	}
	if err != nil {
		return distribution.Proof{}, fmt.Errorf("prove controller: %v", err)
	}
	switch answer.Status {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return distribution.Proof{}, fmt.Errorf("%w: garam answered %d", distribution.ErrNotProved, answer.Status)
	default:
		return distribution.Proof{}, fmt.Errorf("prove controller answered %d: %s", answer.Status, garammachine.FirstLine(answer.Body))
	}
	var p proofJSON
	if err := json.Unmarshal(answer.Body, &p); err != nil {
		return distribution.Proof{}, fmt.Errorf("decode controller proof: %v", err)
	}
	out := distribution.Proof{Operator: p.Operator, Org: p.Org}
	if p.Agent != nil {
		out.Agent = &distribution.ProvenAgent{GRN: p.Agent.GRN, Epoch: p.Agent.Epoch}
	}
	return out, nil
}
