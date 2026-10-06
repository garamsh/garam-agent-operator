package registrar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam creates managed agents on garam's machine listener through createManagedAgent
// (garam@7ca51b9, api/machine.yaml), under managed-enrollment.v1.
type Garam struct {
	machine *garammachine.Client
}

var _ definition.Registrar = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// Register asks garam to create the agent. garam answers one request identifier and reference
// with the same agent however often it is sent, so an attempt garam left undecided is sent again
// by the machine client within its bound; after that, the outcome is unknown.
func (g *Garam) Register(ctx context.Context, r definition.Registration) (definition.Registered, error) {
	answer, err := g.machine.Post(ctx, garammachine.ManagedEnrollment,
		"/operators/"+url.PathEscape(r.Controller)+"/managed-agents", struct {
			RequestID    string `json:"requestId"`
			OperationRef string `json:"operationRef"`
		}{r.Request.RequestID, r.OperationRef})
	if errors.Is(err, garammachine.ErrUndecided) {
		return definition.Registered{}, fmt.Errorf("%w: %v", definition.ErrRegistrationUndecided, err)
	}
	if err != nil {
		return definition.Registered{}, fmt.Errorf("create managed agent: %v", err)
	}
	switch answer.Status {
	case http.StatusCreated, http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return definition.Registered{}, fmt.Errorf("%w: garam answered %d: %s",
			definition.ErrRegistrationRefused, answer.Status, garammachine.FirstLine(answer.Body))
	case http.StatusConflict:
		return definition.Registered{}, fmt.Errorf("%w: garam answered 409: %s",
			definition.ErrRegistrationConflict, garammachine.FirstLine(answer.Body))
	default:
		return definition.Registered{}, fmt.Errorf("create managed agent answered %d: %s",
			answer.Status, garammachine.FirstLine(answer.Body))
	}
	var created struct {
		GRN   string `json:"grn"`
		Epoch string `json:"epoch"`
	}
	if err := json.Unmarshal(answer.Body, &created); err != nil || created.GRN == "" || created.Epoch == "" {
		return definition.Registered{}, fmt.Errorf("decode managed agent: %q", garammachine.FirstLine(answer.Body))
	}
	return definition.Registered{Agent: definition.GRN(created.GRN), Epoch: created.Epoch}, nil
}
