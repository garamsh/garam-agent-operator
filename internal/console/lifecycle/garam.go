package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam calls recoverAgentCredential and deactivateAgent under execution-fence.v1
// (garam@59fe68d api/machine.yaml). Both are safe to send again: a recovery's same requestId, body
// and handoff reference answers the certificate it issued, and a deactivation of an activation
// that already ended is answered the same.
type Garam struct {
	machine *garammachine.Client
}

var _ console.Lifecycle = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// Recover sends body as it is, under `Authorization: Garam-Operation <handoff>`. The handoff is
// never logged.
func (g *Garam) Recover(ctx context.Context, agent string, handoff console.Authority, body []byte) (definition.RecoveredCredential, error) {
	answer, err := g.send(ctx, garammachine.Call{
		Method: http.MethodPost, Contract: garammachine.ExecutionFence,
		Path: "/agents/" + url.PathEscape(agent) + "/credential-recovery", Raw: body,
		Authorization: "Garam-Operation " + string(handoff),
	})
	if err != nil {
		return definition.RecoveredCredential{}, err
	}
	if answer.Status != http.StatusCreated {
		return definition.RecoveredCredential{}, refusal("recovery", answer)
	}
	var out struct {
		GRN            string `json:"grn"`
		Lineage        string `json:"lineage"`
		CertificatePEM string `json:"certificatePem"`
	}
	if err := json.Unmarshal(answer.Body, &out); err != nil {
		return definition.RecoveredCredential{}, fmt.Errorf("decode the recovery: %v", err)
	}
	if out.GRN != agent || out.Lineage == "" || out.CertificatePEM == "" {
		return definition.RecoveredCredential{}, fmt.Errorf("garam answered the recovery of %s for %q, lineage %q",
			agent, out.GRN, out.Lineage)
	}
	return definition.RecoveredCredential{Lineage: out.Lineage, CertificatePEM: out.CertificatePEM}, nil
}

// Deactivate asks garam to end the activation, if it is still the current one.
func (g *Garam) Deactivate(ctx context.Context, agent, activationID string) error {
	answer, err := g.send(ctx, garammachine.Call{
		Method: http.MethodPost, Contract: garammachine.ExecutionFence,
		Path: "/agents/" + url.PathEscape(agent) + "/activations/" + url.PathEscape(activationID) + "/deactivation",
	})
	if err != nil {
		return err
	}
	if answer.Status != http.StatusOK {
		return refusal("deactivation", answer)
	}
	return nil
}

func (g *Garam) send(ctx context.Context, call garammachine.Call) (garammachine.Answer, error) {
	answer, err := g.machine.Send(ctx, call)
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return garammachine.Answer{}, fmt.Errorf("%w: %v", console.ErrLifecycleUndecided, err)
	case errors.As(err, &foreign):
		return garammachine.Answer{}, fmt.Errorf("%w: %v", console.ErrGaramContractUnsupported, err)
	case err != nil:
		return garammachine.Answer{}, fmt.Errorf("call garam %s: %v", call.Path, err)
	}
	return answer, nil
}

// refusal is garam's answer as a *console.LifecycleRefusal where garam refused the request
// itself, and an error of the call's own for any other status.
func refusal(what string, answer garammachine.Answer) error {
	switch answer.Status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity:
		var refused struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(answer.Body, &refused)
		return &console.LifecycleRefusal{Status: answer.Status, Kind: refused.Kind, Message: refused.Message}
	}
	return fmt.Errorf("garam answered the %s %d: %s", what, answer.Status, garammachine.FirstLine(answer.Body))
}
