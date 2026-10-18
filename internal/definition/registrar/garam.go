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

// Garam admits managed agents on garam's machine listener under agent-enrollment.v1
// (garam@37567a4, api/machine.yaml): prepareAgentEnrollment binds an approval, then
// admitAgentEnrollment admits the agent it binds, fenced and in a lineage of its own, in which its
// controller mints the agent's tokens.
type Garam struct {
	machine *garammachine.Client
}

var _ definition.Registrar = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// enrollmentBinding is garam's AgentEnrollmentBinding for a new managed agent: placement is
// always managed, and no replacement is named.
type enrollmentBinding struct {
	RequestID    string `json:"requestId"`
	Placement    string `json:"placement"`
	Assignee     string `json:"assignee"`
	OperationRef string `json:"operationRef"`
}

// Register prepares the approval, then admits the agent it binds. garam answers each stage's
// binding under one request identifier with its first outcome, so a registration interrupted
// between the stages is recovered by registering again: prepare answers the same approval, and
// nothing is kept between them. garam rechecks the reference from current state at both stages,
// so one it no longer authorizes is refused on that retry as on a first attempt. An attempt garam
// left undecided is sent again by the machine client within its bound; after that, the outcome is
// unknown.
func (g *Garam) Register(ctx context.Context, r definition.Registration) (definition.Registered, error) {
	binding := enrollmentBinding{
		RequestID: r.Request.RequestID, Placement: "managed", Assignee: r.Controller, OperationRef: r.OperationRef,
	}
	var approval struct {
		ApprovalID string `json:"approvalId"`
	}
	if err := g.enroll(ctx, "prepare agent enrollment", "/agent-enrollment-approvals", binding, &approval); err != nil {
		return definition.Registered{}, err
	}
	if approval.ApprovalID == "" {
		return definition.Registered{}, errors.New("prepare agent enrollment answered no approval")
	}
	var admitted struct {
		GRN   string `json:"grn"`
		Epoch string `json:"epoch"`
	}
	if err := g.enroll(ctx, "admit agent enrollment",
		"/agent-enrollment-approvals/"+url.PathEscape(approval.ApprovalID)+"/admission", binding, &admitted); err != nil {
		return definition.Registered{}, err
	}
	if admitted.GRN == "" || admitted.Epoch == "" {
		return definition.Registered{}, errors.New("admit agent enrollment answered no agent or epoch")
	}
	return definition.Registered{Agent: definition.GRN(admitted.GRN), Epoch: admitted.Epoch}, nil
}

// enroll sends binding to one stage and decodes its 201 or 200 into answer. 403 and 404 are
// refusals and 409 a conflict, each carrying garam's reason where it names one.
func (g *Garam) enroll(ctx context.Context, stage, path string, binding enrollmentBinding, answer any) error {
	answered, err := g.machine.Post(ctx, garammachine.AgentEnrollment, path, binding)
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return fmt.Errorf("%w: %s: %v", definition.ErrRegistrationUndecided, stage, err)
	case errors.As(err, &foreign):
		return fmt.Errorf("%w: %s: %v", definition.ErrGaramContractUnsupported, stage, err)
	case err != nil:
		return fmt.Errorf("%s: %v", stage, err)
	}
	switch answered.Status {
	case http.StatusCreated, http.StatusOK:
		if err := json.Unmarshal(answered.Body, answer); err != nil {
			return fmt.Errorf("decode %s: %q", stage, garammachine.FirstLine(answered.Body))
		}
		return nil
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w: %s answered %d%s", definition.ErrRegistrationRefused, stage, answered.Status, refusal(answered.Body))
	case http.StatusConflict:
		return fmt.Errorf("%w: %s answered 409%s", definition.ErrRegistrationConflict, stage, refusal(answered.Body))
	default:
		return fmt.Errorf("%s answered %d: %s", stage, answered.Status, garammachine.FirstLine(answered.Body))
	}
}

// refusal is garam's error body as one line, after the reason it names, if any.
func refusal(body []byte) string {
	var refused struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &refused) == nil && refused.Reason != "" {
		return ", reason " + refused.Reason + ": " + garammachine.FirstLine(body)
	}
	return ": " + garammachine.FirstLine(body)
}
