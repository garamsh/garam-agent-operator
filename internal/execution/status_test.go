package execution_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/execution"
)

// statusBody is the runtime's report of gen at revision in state, under activationID.
func statusBody(activationID, gen, revision, state string) string {
	b, _ := json.Marshal(map[string]string{
		"activationId": activationID, "grn": agent, keyEpoch: epoch, keyGeneration: gen, keyConfigRevision: revision,
		"state": state, "startedAt": "2026-10-05T10:00:00Z", "observedAt": "2026-10-05T10:00:05Z",
	})
	return string(b)
}

// report sends a runtime-status report with client.
func (e *env) report(t *testing.T, client *http.Client, body string) answer {
	t.Helper()
	return e.post(t, client, "runtime-status", body, "", nil)
}

// activated is an env whose agent's generation is activated as activation-1.
func activated(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	require.Equal(t, http.StatusCreated, e.activate(t, e.adapter, requestID, generation, "1").status)
	return e
}

func (e *env) applied(t *testing.T) (definition.RuntimeApplied, bool) {
	t.Helper()
	applied, err := e.repository.GetRuntimeApplied(context.Background(), agent)
	if err != nil {
		require.ErrorIs(t, err, definition.ErrNotFound)
		return definition.RuntimeApplied{}, false
	}
	return applied, true
}

func TestReportStatus_AcceptsTheActivatedGenerationAndRecordsWhatItServes(t *testing.T) {
	e := activated(t)
	body := statusBody(firstActivation, generation, "1", "serving")

	for range 2 {
		accepted := e.report(t, e.adapter, body)
		require.Equal(t, http.StatusNoContent, accepted.status, accepted.raw)
		assert.Equal(t, execution.Contract, accepted.contract)
	}
	applied, ok := e.applied(t)
	require.True(t, ok)
	assert.Equal(t, definition.RuntimeApplied{Revision: 1, ActivationID: firstActivation, Generation: generation,
		ObservedAt: time.Date(2026, 10, 5, 10, 0, 5, 0, time.UTC)}, applied)
	assert.Equal(t, 1, e.garam.callCount(), "a report activated something")
}

func TestReportStatus_AnUnknownRevisionIsNoEvidence(t *testing.T) {
	for name, body := range map[string]string{
		"an empty revision":  statusBody(firstActivation, generation, "", "serving"),
		"a draining runtime": statusBody(firstActivation, generation, "1", "draining"),
		"a revision unknown": statusBody(firstActivation, generation, "9", "serving"),
	} {
		t.Run(name, func(t *testing.T) {
			e := activated(t)
			accepted := e.report(t, e.adapter, body)
			require.Equal(t, http.StatusNoContent, accepted.status, accepted.raw)
			_, ok := e.applied(t)
			assert.False(t, ok, "the revision was recorded as applied")

			// Control: a serving runtime reporting a revision the agent has is recorded.
			e.report(t, e.adapter, statusBody(firstActivation, generation, "1", "serving"))
			_, ok = e.applied(t)
			assert.True(t, ok)
		})
	}
}

func TestReportStatus_RefusesWhatTheContractRefuses(t *testing.T) {
	valid := statusBody(firstActivation, generation, "1", "serving")
	tests := []struct {
		name   string
		setup  func(e *env)
		body   string
		status int
		kind   string
	}{
		{"another generation than the active one", nil, statusBody(firstActivation, strings.Repeat("b", 32), "1", "serving"),
			http.StatusConflict, kindGenerationNotCurrent},
		{"another activation than the active one", nil, statusBody("activation-9", generation, "1", "serving"),
			http.StatusConflict, kindGenerationNotCurrent},
		{"another epoch than garam's", func(e *env) { e.garam.set(func(g *garam) { g.epoch = "8" }) }, valid,
			http.StatusConflict, kindGenerationNotCurrent},
		{"an agent assigned elsewhere", func(e *env) {
			e.garam.set(func(g *garam) { g.assignee = "grn:acme:default:operator:other" })
		}, valid, http.StatusConflict, kindGenerationNotCurrent},
		{"an ended activation", func(e *env) { e.garam.end() }, valid, http.StatusConflict, kindGenerationNotCurrent},
		{"garam answering the generation not current", func(e *env) {
			e.garam.set(func(g *garam) { g.generationAnswer = "not_current" })
		}, valid, http.StatusConflict, kindGenerationNotCurrent},
		{"garam holding another activation of the generation", func(e *env) {
			e.garam.set(func(g *garam) { g.active = &activation{id: "activation-9", generation: generation} })
		}, valid, http.StatusConflict, kindGenerationNotCurrent},
		{"an activation control did not make", func(e *env) {
			e.garam.set(func(g *garam) { g.active = &activation{id: "activation-9", generation: generation} })
		}, statusBody("activation-9", generation, "1", "serving"), http.StatusConflict, kindGenerationNotCurrent},
		{"a fenced credential", func(e *env) { e.garam.set(func(g *garam) { g.fenced[string(e.leafPEM)] = true }) }, valid,
			http.StatusForbidden, kindCredentialFenced},
		{"garam refusing control's authority", func(e *env) {
			e.garam.set(func(g *garam) { g.introspect = execution.ErrNotAuthorized })
		}, valid, http.StatusForbidden, kindCredentialFenced},
		{"garam undecided", func(e *env) { e.garam.set(func(g *garam) { g.introspect = execution.ErrUndecided }) }, valid,
			http.StatusServiceUnavailable, kindUndecided},
		{"another agent named in the body", nil, strings.Replace(valid, agent, "grn:acme:default:agent:other", 1),
			http.StatusBadRequest, kindInvalidRequest},
		{"a state the runtime does not report", nil, statusBody(firstActivation, generation, "1", "stopped"),
			http.StatusBadRequest, kindInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := activated(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			refused := e.report(t, e.adapter, tt.body)
			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.kind, refused.kind())
			_, ok := e.applied(t)
			assert.False(t, ok, "a refused report was recorded")

			// Control: a fresh env's report of the activated generation is accepted.
			fresh := activated(t)
			assert.Equal(t, http.StatusNoContent, fresh.report(t, fresh.adapter, valid).status)
		})
	}
}

func TestReportStatus_NeverActivatesAnything(t *testing.T) {
	e := newEnv(t)
	refused := e.report(t, e.adapter, statusBody(firstActivation, generation, "1", "serving"))
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, 0, e.garam.callCount())

	// Control: once activated, the same report is accepted.
	require.Equal(t, http.StatusCreated, e.activate(t, e.adapter, requestID, generation, "1").status)
	assert.Equal(t, http.StatusNoContent, e.report(t, e.adapter, statusBody(firstActivation, generation, "1", "serving")).status)
}
