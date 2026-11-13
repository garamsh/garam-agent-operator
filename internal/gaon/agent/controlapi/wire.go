package controlapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// The wire of contract C2's desired feed, in the form
// garamsh/garam-agent-operator docs/architecture/control.md states.
// Every field the operator reads is a pointer, so an absent one is told
// from a zero one, except those of configuration: an absent one there
// is one the agent is configured without. What a configuration may hold
// is decided per agent where it is rendered, so that one agent's
// malformed configuration does not stop the others.
type (
	wireAnswer struct {
		Cursor *string      `json:"cursor"`
		Agents *[]wireAgent `json:"agents"`
	}
	wireAgent struct {
		Agent         *string         `json:"agent"`
		Revision      *string         `json:"revision"`
		Epoch         *string         `json:"epoch"`
		Profile       *wireProfile    `json:"profile"`
		Configuration json.RawMessage `json:"configuration"`
		Stopped       bool            `json:"stopped"`
		Recovery      *wireRecovery   `json:"recovery"`
	}
	wireRecovery struct {
		RequestID *string `json:"requestId"`
		Epoch     *string `json:"epoch"`
	}
	wireConfiguration struct {
		Model wireModel         `json:"model"`
		Ego   string            `json:"ego"`
		Tools map[string]string `json:"tools"`
	}
	wireModel struct {
		Provider  string         `json:"provider"`
		BaseURL   string         `json:"baseUrl"`
		Name      string         `json:"name"`
		APIKeyRef string         `json:"apiKeyRef"`
		Embedding *wireEmbedding `json:"embedding"`
	}
	wireEmbedding struct {
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		APIKeyRef string `json:"apiKeyRef"`
	}
	wireProfile struct {
		Name                 *string         `json:"name"`
		Version              *int64          `json:"version"`
		Resources            json.RawMessage `json:"resources"`
		StorageSize          *string         `json:"storageSize"`
		StorageClassName     *string         `json:"storageClassName"`
		WorkspaceStorageSize *string         `json:"workspaceStorageSize"`
	}
)

// answer is a feed answer that was read whole.
type answer struct {
	cursor string
	agents []agent.Desired
}

// parseAnswer reads body as a whole feed answer. It returns an error
// for anything it cannot read in full, and never an empty set in its
// place: only `"agents": []` is an empty set, and an empty set halts
// every agent the operator owns.
func parseAnswer(body []byte) (answer, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var wire wireAnswer
	if err := dec.Decode(&wire); err != nil {
		return answer{}, errorx.Wrap(err, "feed answer is not the contract's", errorx.KindInternal)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return answer{}, errorx.New(errorx.KindInternal, "feed answer has data after its object")
	}
	if wire.Cursor == nil || *wire.Cursor == "" {
		return answer{}, errorx.New(errorx.KindInternal, "feed answer has no cursor")
	}
	if wire.Agents == nil {
		return answer{}, errorx.New(errorx.KindInternal, "feed answer has no agents list")
	}
	out := answer{cursor: *wire.Cursor, agents: make([]agent.Desired, 0, len(*wire.Agents))}
	seen := map[string]bool{}
	for i, w := range *wire.Agents {
		d, err := w.parse()
		if err != nil {
			return answer{}, errorx.Wrap(err, "feed agent "+strconv.Itoa(i))
		}
		if seen[d.GRN.String()] {
			return answer{}, errorx.New(errorx.KindInternal, "feed names agent "+d.GRN.String()+" twice")
		}
		seen[d.GRN.String()] = true
		out.agents = append(out.agents, d)
	}
	return out, nil
}

func (w wireAgent) parse() (agent.Desired, error) {
	if w.Agent == nil || w.Revision == nil || w.Epoch == nil || w.Profile == nil || !isObject(w.Configuration) {
		return agent.Desired{}, errorx.New(errorx.KindInternal, "agent lacks a field the contract requires")
	}
	g, err := grn.Parse(*w.Agent)
	if err != nil || g.ServiceType != "agent" || g.Locator != "" || g.String() != *w.Agent {
		return agent.Desired{}, errorx.New(errorx.KindInternal, "agent is not a canonical agent GRN")
	}
	revision, err := canonical(*w.Revision)
	if err != nil {
		return agent.Desired{}, errorx.Wrap(err, "revision")
	}
	epoch, err := canonical(*w.Epoch)
	if err != nil {
		return agent.Desired{}, errorx.Wrap(err, "epoch")
	}
	if w.Recovery != nil && (w.Recovery.RequestID == nil || w.Recovery.Epoch == nil) {
		return agent.Desired{}, errorx.New(errorx.KindInternal, "recovery lacks a field the contract requires")
	}
	memory, workspace, err := w.Profile.sizes()
	if err != nil {
		return agent.Desired{}, err
	}
	configuration, err := parseConfiguration(w.Configuration)
	if err != nil {
		return agent.Desired{}, err
	}
	return agent.Desired{
		GRN: g, Epoch: epoch, Revision: strconv.FormatInt(revision, 10), Stopped: w.Stopped,
		MemoryStorage: memory, WorkspaceStorage: workspace, Configuration: configuration,
	}, nil
}

// parseConfiguration reads the types of configuration. The contract
// writes `{}` for the model of an agent with none, which is no model.
func parseConfiguration(raw json.RawMessage) (agent.Configuration, error) {
	var wire wireConfiguration
	if err := json.Unmarshal(raw, &wire); err != nil {
		return agent.Configuration{}, errorx.Wrap(err, "configuration is not the contract's", errorx.KindInternal)
	}
	out := agent.Configuration{Ego: wire.Ego, Tools: wire.Tools}
	m := wire.Model
	if m.Provider == "" && m.BaseURL == "" && m.Name == "" && m.APIKeyRef == "" && m.Embedding == nil {
		return out, nil
	}
	out.Model = &agent.Model{Provider: m.Provider, BaseURL: m.BaseURL, Name: m.Name, APIKeyRef: m.APIKeyRef}
	if e := m.Embedding; e != nil {
		out.Model.Embedding = &agent.Embedding{BaseURL: e.BaseURL, Name: e.Name, APIKeyRef: e.APIKeyRef}
	}
	return out, nil
}

// sizes returns the profile's two Volume sizes. A profile that names no
// workspace size gives the workspace the memory Volume's.
func (p wireProfile) sizes() (api.Quantity, api.Quantity, error) {
	if p.Name == nil || *p.Name == "" || p.Version == nil || !isObject(p.Resources) || p.StorageSize == nil {
		return "", "", errorx.New(errorx.KindInternal, "profile lacks a field the contract requires")
	}
	memory, err := api.ParseQuantity(*p.StorageSize)
	if err != nil {
		return "", "", errorx.Wrap(err, "profile storageSize", errorx.KindInternal)
	}
	if p.WorkspaceStorageSize == nil {
		return memory, memory, nil
	}
	workspace, err := api.ParseQuantity(*p.WorkspaceStorageSize)
	if err != nil {
		return "", "", errorx.Wrap(err, "profile workspaceStorageSize", errorx.KindInternal)
	}
	return memory, workspace, nil
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// canonical reads s as the canonical decimal string contract C2 writes
// every revision and epoch as: digits, no sign, no leading zero.
func canonical(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, errorx.New(errorx.KindInternal, "is not a canonical decimal string")
	}
	return n, nil
}
