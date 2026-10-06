package cutover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam carries a legacy agent's cutover stages to garam's machine listener under
// agent-cutover.v1 (garam@1a5273d, api/machine.yaml §cutover): getAgentCutover,
// freezeAgentCutover, switchAgentCutover and rollBackAgentCutover. Each is sent under
// `Authorization: Garam-Operation <ref>`, the durable reference minted for that stage's route, and
// each answers a repeat of one importId with what it recorded, so the machine client's retry of an
// undecided attempt is safe.
type Garam struct {
	machine *garammachine.Client
}

var _ console.Cutover = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// suffixes is each stage's route below the agent's cutover.
var suffixes = map[console.Stage]string{
	console.StageRead: "", console.StageFreeze: "/freeze", console.StageSwitch: "/switch", console.StageRollback: "/rollback",
}

func path(agent string, stage console.Stage) string {
	return "/agents/" + url.PathEscape(agent) + "/cutover" + suffixes[stage]
}

// Target is the stage's route as garam receives it.
func (g *Garam) Target(agent string, stage console.Stage) string {
	return g.machine.Target(path(agent, stage))
}

// Read asks getAgentCutover for the agent's source and its digest.
func (g *Garam) Read(ctx context.Context, agent, ref string) (console.CutoverSource, error) {
	var out struct {
		Agent    string `json:"agent"`
		Assignee string `json:"assignee"`
		Epoch    string `json:"epoch"`
		Mode     string `json:"mode"`
		Class    string `json:"class"`
		Source   *struct {
			Operator string            `json:"operator"`
			Values   map[string]string `json:"values"`
		} `json:"source"`
		SourceDigest *string `json:"sourceDigest"`
	}
	if err := g.send(ctx, http.MethodGet, agent, console.StageRead, ref, nil, &out); err != nil {
		return console.CutoverSource{}, err
	}
	source := console.CutoverSource{Agent: out.Agent, Assignee: out.Assignee, Epoch: out.Epoch, Mode: out.Mode, Class: out.Class}
	if out.Source != nil {
		source.HasSource, source.Operator, source.Values = true, out.Source.Operator, out.Source.Values
	}
	if out.SourceDigest != nil {
		source.SourceDigest = *out.SourceDigest
	}
	return source, nil
}

// Freeze asks freezeAgentCutover to freeze the source against the import's digest.
func (g *Garam) Freeze(ctx context.Context, agent, ref, importID, sourceDigest, epoch string) (console.CutoverAttempt, error) {
	return g.attempt(ctx, agent, console.StageFreeze, ref, struct {
		ImportID     string `json:"importId"`
		SourceDigest string `json:"sourceDigest"`
		Epoch        string `json:"epoch"`
	}{importID, sourceDigest, epoch})
}

// Switch asks switchAgentCutover to record the switch.
func (g *Garam) Switch(ctx context.Context, agent, ref, importID string) (console.CutoverAttempt, error) {
	return g.attempt(ctx, agent, console.StageSwitch, ref, stageBody{importID})
}

// RollBack asks rollBackAgentCutover to end the frozen attempt.
func (g *Garam) RollBack(ctx context.Context, agent, ref, importID string) (console.CutoverAttempt, error) {
	return g.attempt(ctx, agent, console.StageRollback, ref, stageBody{importID})
}

type stageBody struct {
	ImportID string `json:"importId"`
}

func (g *Garam) attempt(ctx context.Context, agent string, stage console.Stage, ref string, body any) (console.CutoverAttempt, error) {
	var out struct {
		ImportID     string `json:"importId"`
		Stage        string `json:"stage"`
		FrozenDigest string `json:"frozenDigest"`
	}
	if err := g.send(ctx, http.MethodPost, agent, stage, ref, body, &out); err != nil {
		return console.CutoverAttempt{}, err
	}
	return console.CutoverAttempt{ImportID: out.ImportID, Stage: out.Stage, FrozenDigest: out.FrozenDigest}, nil
}

// send makes one stage's call and decodes a 200 or 201 into out. A refusal is a
// *console.CutoverRefusal under garam's reason, or its kind where it names none.
func (g *Garam) send(ctx context.Context, method, agent string, stage console.Stage, ref string, body, out any) error {
	answer, err := g.machine.Send(ctx, garammachine.Call{
		Method: method, Contract: garammachine.AgentCutover, Path: path(agent, stage), Body: body,
		Authorization: "Garam-Operation " + ref,
	})
	if errors.Is(err, garammachine.ErrUndecided) {
		return fmt.Errorf("%w: %v", console.ErrCutoverUndecided, err)
	}
	if err != nil {
		return fmt.Errorf("cutover %s: %v", stage, err)
	}
	switch answer.Status {
	case http.StatusOK, http.StatusCreated:
		if err := json.Unmarshal(answer.Body, out); err != nil {
			return fmt.Errorf("decode cutover %s: %v", stage, err)
		}
		return nil
	case http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		var refused struct {
			Kind    string `json:"kind"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(answer.Body, &refused)
		kind := refused.Reason
		if kind == "" {
			kind = refused.Kind
		}
		return &console.CutoverRefusal{Status: answer.Status, Kind: kind, Reason: refused.Reason, Message: refused.Message}
	}
	return fmt.Errorf("cutover %s answered %d: %s", stage, answer.Status, garammachine.FirstLine(answer.Body))
}
