package garam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/garamsh/garam-agent-operator/internal/execution"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam asks garam's machine listener what the agent routes decide on (garam@afde8b8,
// api/machine.yaml): introspectAgentExecution and activateAgent under execution-fence.v2, each
// forwarding the agent's token minted for this service as token. An attempt garam leaves undecided
// is sent again by the machine client within its bound; activateAgent answers a replay of one
// requestId with the same activation, so that is safe too.
type Garam struct {
	machine *garammachine.Client
}

var _ execution.Garam = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// Introspect asks introspectAgentExecution about the token, and the generation where one is given.
func (g *Garam) Introspect(ctx context.Context, agent string, token execution.AgentToken, generation string) (execution.Introspection, error) {
	in := struct {
		Token      string  `json:"token"`
		Generation *string `json:"generation"`
	}{Token: string(token)}
	if generation != "" {
		in.Generation = &generation
	}
	answer, err := g.post(ctx, garammachine.ExecutionFence, "/agents/"+url.PathEscape(agent)+"/execution/introspection", in)
	if err != nil {
		return execution.Introspection{}, err
	}
	var out struct {
		GRN          string  `json:"grn"`
		Org          string  `json:"org"`
		Assignee     string  `json:"assignee"`
		Epoch        string  `json:"epoch"`
		Mode         string  `json:"mode"`
		Credential   string  `json:"credential"`
		Generation   string  `json:"generation"`
		ActivationID *string `json:"activationId"`
	}
	if err := decode(answer, http.StatusOK, &out); err != nil {
		return execution.Introspection{}, err
	}
	i := execution.Introspection{
		GRN: out.GRN, Org: out.Org, Assignee: out.Assignee, Epoch: out.Epoch,
		Mode: out.Mode, Credential: out.Credential, Generation: out.Generation,
	}
	if out.ActivationID != nil {
		i.ActivationID = *out.ActivationID
	}
	return i, nil
}

// Activate asks activateAgent, and reports whether garam created the activation.
func (g *Garam) Activate(ctx context.Context, agent string, call execution.ActivationCall) (execution.Activation, bool, error) {
	in := struct {
		RequestID            string  `json:"requestId"`
		Epoch                string  `json:"epoch"`
		Generation           string  `json:"generation"`
		ReplacesActivationID *string `json:"replacesActivationId"`
		OperationRef         *string `json:"operationRef"`
		Token                string  `json:"token"`
	}{RequestID: call.RequestID, Epoch: call.Epoch, Generation: call.Generation, Token: string(call.Token)}
	if call.ReplacesActivationID != "" {
		in.ReplacesActivationID = &call.ReplacesActivationID
	}
	if call.OperationRef != "" {
		in.OperationRef = &call.OperationRef
	}
	answer, err := g.post(ctx, garammachine.ExecutionFence, "/agents/"+url.PathEscape(agent)+"/activations", in)
	if err != nil {
		return execution.Activation{}, false, err
	}
	var out struct {
		ActivationID string `json:"activationId"`
		Token        string `json:"token"`
		TokenVersion int    `json:"tokenVersion"`
		GRN          string `json:"grn"`
		Epoch        string `json:"epoch"`
		Generation   string `json:"generation"`
	}
	created := answer.Status == http.StatusCreated
	if created {
		answer.Status = http.StatusOK
	}
	if err := decode(answer, http.StatusOK, &out); err != nil {
		return execution.Activation{}, false, err
	}
	return execution.Activation{
		ActivationID: out.ActivationID, Token: out.Token, TokenVersion: out.TokenVersion,
		GRN: out.GRN, Epoch: out.Epoch, Generation: out.Generation,
	}, created, nil
}

// post sends in, and answers garam's undecided as execution.ErrUndecided and an answer under
// another contract as a *execution.GaramContractError.
func (g *Garam) post(ctx context.Context, contract, path string, in any) (garammachine.Answer, error) {
	answer, err := g.machine.Post(ctx, contract, path, in)
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return garammachine.Answer{}, fmt.Errorf("%w: %v", execution.ErrUndecided, err)
	case errors.As(err, &foreign):
		return garammachine.Answer{}, &execution.GaramContractError{Call: foreign.Path, Contract: foreign.Got}
	}
	if err != nil {
		return garammachine.Answer{}, fmt.Errorf("call garam %s: %v", path, err)
	}
	return answer, nil
}

// decode reads an answer of status into out, and the refusals as the port names them. A refusal's
// body is not read past its first line: an activation's never carries a token.
func decode(answer garammachine.Answer, status int, out any) error {
	switch answer.Status {
	case status:
	case http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w: garam answered %d", execution.ErrNotAuthorized, answer.Status)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: garam answered 422", execution.ErrCredentialRefused)
	case http.StatusConflict:
		return fmt.Errorf("%w: garam answered 409", execution.ErrActivationSuperseded)
	default:
		return fmt.Errorf("garam answered %d: %s", answer.Status, garammachine.FirstLine(answer.Body))
	}
	if err := json.Unmarshal(answer.Body, out); err != nil {
		return fmt.Errorf("decode garam's answer: %v", err)
	}
	return nil
}
