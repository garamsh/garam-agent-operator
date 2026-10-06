package execution_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/execution"
)

const requestID = "737a3319-e377-8fab-ba8c-fe01762adb62"

func TestActivate_ActivatesAndRemintsTheSameActivationOnRetry(t *testing.T) {
	e := newEnv(t)

	first := e.activate(t, e.adapter, requestID, generation, "1")
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	assert.Equal(t, execution.Contract, first.contract)
	assert.Equal(t, map[string]any{
		"activationId": firstActivation, "tokenVersion": float64(1), "token": "token-activation-1-1",
		"grn": agent, keyEpoch: epoch, keyGeneration: generation, keyConfigRevision: "1",
	}, first.body)

	// The adapter's retry after a lost answer: the same activation, a newer token.
	retry := e.activate(t, e.adapter, requestID, generation, "1")
	require.Equal(t, http.StatusOK, retry.status, retry.raw)
	assert.Equal(t, firstActivation, retry.body["activationId"])
	assert.Equal(t, float64(2), retry.body["tokenVersion"])

	// Both attempts sent garam the same request, the first activation under the creation's reference.
	require.Equal(t, 2, e.garam.callCount())
	assert.Equal(t, e.garam.calls[0], e.garam.calls[1])
	assert.Equal(t, execution.ActivationCall{RequestID: requestID, Epoch: epoch, Generation: generation,
		OperationRef: createRef, CertificatePEM: e.leafPEM}, e.garam.calls[0])
}

func TestActivate_UndecidedIsRetriedAsTheSameActivation(t *testing.T) {
	e := newEnv(t)
	e.garam.set(func(g *garam) { g.activate = fmt.Errorf("no answer: %w", execution.ErrUndecided) })
	undecided := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusServiceUnavailable, undecided.status, undecided.raw)
	assert.Equal(t, kindUndecided, undecided.kind())

	// Control: once garam answers, the same request is activated and sent unchanged.
	e.garam.set(func(g *garam) { g.activate = nil })
	activated := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusCreated, activated.status, activated.raw)
	require.Equal(t, 2, e.garam.callCount())
	assert.Equal(t, e.garam.calls[0], e.garam.calls[1])
}

func TestActivate_RefusesWhatTheContractRefuses(t *testing.T) {
	type refusal struct {
		name   string
		setup  func(t *testing.T, e *env)
		send   func(t *testing.T, e *env) answer
		status int
		kind   string
	}
	activate := func(t *testing.T, e *env) answer { return e.activate(t, e.adapter, requestID, generation, "1") }
	tests := []refusal{
		{"another contract version", nil, func(t *testing.T, e *env) answer {
			return e.post(t, e.adapter, "activations", activationBody(requestID, generation, "1"), token,
				map[string]string{"Garam-Contract-Version": "agent-execution.v0"})
		}, http.StatusBadRequest, kindInvalidRequest},
		{"no agent certificate", nil, func(t *testing.T, e *env) answer {
			return e.post(t, e.server.Client(), "activations", activationBody(requestID, generation, "1"), token, nil)
		}, http.StatusUnauthorized, kindUnauthenticated},
		{"a certificate naming another agent", nil, func(t *testing.T, e *env) answer {
			other, _ := e.leaf(t, "grn:acme:default:agent:other")
			return e.activate(t, other, requestID, generation, "1")
		}, http.StatusForbidden, kindNotAuthorized},
		{"no placement token", nil, func(t *testing.T, e *env) answer {
			return e.post(t, e.adapter, "activations", activationBody(requestID, generation, "1"), "", nil)
		}, http.StatusUnauthorized, kindUnauthenticated},
		{"another placement's token", nil, func(t *testing.T, e *env) answer {
			return e.post(t, e.adapter, "activations", activationBody(requestID, generation, "1"), "another token", nil)
		}, http.StatusForbidden, kindPlacementNotCurrent},
		{"another epoch than the placement's", nil, func(t *testing.T, e *env) answer {
			return e.post(t, e.adapter, "activations",
				strings.Replace(activationBody(requestID, generation, "1"), `"epoch":"7"`, `"epoch":"6"`, 1), token, nil)
		}, http.StatusConflict, kindEpochSuperseded},
		{"a revision the agent does not have", nil, func(t *testing.T, e *env) answer {
			return e.activate(t, e.adapter, requestID, generation, "9")
		}, http.StatusBadRequest, kindInvalidRequest},
		{"a fenced credential", func(_ *testing.T, e *env) { e.garam.set(func(g *garam) { g.fenced[string(e.leafPEM)] = true }) },
			activate, http.StatusForbidden, kindCredentialFenced},
		{"a leaf garam refuses as not the agent's", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.introspect = execution.ErrCredentialRefused })
		}, activate, http.StatusForbidden, kindCredentialFenced},
		{"an agent assigned elsewhere", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.assignee = "grn:acme:default:operator:other" })
		}, activate, http.StatusForbidden, kindPlacementNotCurrent},
		{"garam's epoch another than the placement's", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.epoch, g.proveEpoch = "8", epoch })
		},
			activate, http.StatusConflict, kindEpochSuperseded},
		{"garam refusing control's authority", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.introspect = execution.ErrNotAuthorized })
		}, activate, http.StatusForbidden, kindNotAuthorized},
		{"garam refusing the controller's proof", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.prove = execution.ErrNotAuthorized })
		}, activate, http.StatusForbidden, kindNotAuthorized},
		{"garam undecided on the introspection", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.introspect = execution.ErrUndecided })
		}, activate, http.StatusServiceUnavailable, kindUndecided},
		{"garam refusing the activation as superseded", func(_ *testing.T, e *env) {
			e.garam.set(func(g *garam) { g.activate = execution.ErrActivationSuperseded })
		}, activate, http.StatusConflict, kindActivationSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(t, e)
			}
			refused := tt.send(t, e)
			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.kind, refused.kind())
			assert.Equal(t, execution.Contract, refused.contract, "a refusal was answered without the contract")

			// Control: the same activation, from the agent's current leaf on its placement, as garam
			// answers it unrefused.
			e.garam.set(func(g *garam) {
				g.fenced, g.assignee, g.epoch = map[string]bool{}, controller, epoch
				g.introspect, g.prove, g.activate, g.proveEpoch = nil, nil, nil, ""
			})
			accepted := e.activate(t, e.adapter, requestID, generation, "1")
			assert.Contains(t, []int{http.StatusCreated, http.StatusOK}, accepted.status, accepted.raw)
		})
	}
}

func TestActivate_RequestIDReusedForAnotherActivationRefused(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, http.StatusCreated, e.activate(t, e.adapter, requestID, generation, "1").status)

	reused := e.activate(t, e.adapter, requestID, strings.Repeat("b", 32), "1")
	assert.Equal(t, http.StatusConflict, reused.status, reused.raw)
	assert.Equal(t, kindRequestReused, reused.kind())

	// Control: the request as it was made is answered.
	assert.Equal(t, http.StatusOK, e.activate(t, e.adapter, requestID, generation, "1").status)
}

func TestActivate_RefusesWhatIsNotOneActivation(t *testing.T) {
	valid := activationBody(requestID, generation, "1")
	for name, body := range map[string]string{
		"a request id garam does not take": activationBody("not one", generation, "1"),
		"a generation that is not 32 hex":  activationBody(requestID, "ABC", "1"),
		"a revision that is not decimal":   activationBody(requestID, generation, "one"),
		"an unknown field":                 strings.Replace(valid, `{`, `{"token":"x",`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			refused := e.post(t, e.adapter, "activations", body, token, nil)
			assert.Equal(t, http.StatusBadRequest, refused.status, refused.raw)
			assert.Equal(t, kindInvalidRequest, refused.kind())

			// Control: the valid activation is accepted.
			assert.Equal(t, http.StatusCreated, e.post(t, e.adapter, "activations", valid, token, nil).status)
		})
	}
}

// TestActivate_SendsTheReferenceOfTheRevisionItRuns holds the #218 rule: a revision the runtime
// reported effective under the latest activation is activated under no reference; a pending one
// keeps its durable reference, so a restart of it is refused once that reference's actor is gone.
func TestActivate_SendsTheReferenceOfTheRevisionItRuns(t *testing.T) {
	e := newEnv(t)
	e.configure(t, 1, "configure-ref-2")
	require.Equal(t, http.StatusCreated, e.activate(t, e.adapter, requestID, generation, "1").status)

	// Revision 2 activated, then the runtime fails before reporting it, and its actor is revoked.
	second := strings.Repeat("b", 32)
	require.Equal(t, http.StatusCreated, e.activate(t, e.adapter, "request-2", second, "2").status)
	assert.Equal(t, "configure-ref-2", e.garam.calls[1].OperationRef)
	assert.Equal(t, firstActivation, e.garam.calls[1].ReplacesActivationID)
	e.garam.set(func(g *garam) { g.refusedRefs["configure-ref-2"] = true })

	restart := e.activate(t, e.adapter, "request-3", strings.Repeat("c", 32), "2")
	assert.Equal(t, http.StatusForbidden, restart.status, restart.raw)
	assert.Equal(t, "configure-ref-2", e.garam.calls[2].OperationRef, "the pending revision was sent under no reference")

	// Control: once the runtime reports revision 2 serving under the latest activation, a restart of
	// it is activated under no reference.
	report := e.report(t, e.adapter, statusBody("activation-2", second, "2", "serving"))
	require.Equal(t, http.StatusNoContent, report.status, report.raw)
	restarted := e.activate(t, e.adapter, "request-4", strings.Repeat("d", 32), "2")
	assert.Equal(t, http.StatusCreated, restarted.status, restarted.raw)
	assert.Empty(t, e.garam.calls[3].OperationRef)
	assert.Equal(t, "activation-2", e.garam.calls[3].ReplacesActivationID)
}

// TestActivate_RenewalExpiryAndRestart is #218's required sequence: a renewed leaf remints the same
// activation at a higher token version; the expired first leaf, the recovered old lineage and an
// ended activation all refuse.
func TestActivate_RenewalExpiryAndRestart(t *testing.T) {
	e := newEnv(t)
	first := e.activate(t, e.adapter, requestID, generation, "1")
	require.Equal(t, http.StatusCreated, first.status, first.raw)

	// The agent renews its certificate: the renewed leaf remints the same activation.
	renewed, renewedPEM := e.leaf(t, agent)
	again := e.activate(t, renewed, requestID, generation, "1")
	require.Equal(t, http.StatusOK, again.status, again.raw)
	assert.Equal(t, first.body["activationId"], again.body["activationId"])
	assert.Equal(t, float64(2), again.body["tokenVersion"])
	assert.Equal(t, renewedPEM, e.garam.calls[1].CertificatePEM, "the leaf forwarded was not this request's")

	// The first leaf expires: garam holds it fenced.
	e.garam.set(func(g *garam) { g.fenced[string(e.leafPEM)] = true })
	expired := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusForbidden, expired.status, expired.raw)
	assert.Equal(t, kindCredentialFenced, expired.kind())

	// The adapter restarts and replays its own activation under the renewed leaf.
	restarted := e.activate(t, renewed, requestID, generation, "1")
	require.Equal(t, http.StatusOK, restarted.status, restarted.raw)
	assert.Equal(t, float64(3), restarted.body["tokenVersion"])

	// A recovered lineage fences the renewed leaf and ends the activation it held.
	recovered, _ := e.leaf(t, agent)
	e.garam.set(func(g *garam) { g.fenced[string(renewedPEM)] = true })
	e.garam.end()
	oldLineage := e.activate(t, renewed, requestID, generation, "1")
	assert.Equal(t, http.StatusForbidden, oldLineage.status, oldLineage.raw)
	assert.Equal(t, kindCredentialFenced, oldLineage.kind())
	ended := e.activate(t, recovered, requestID, generation, "1")
	assert.Equal(t, http.StatusConflict, ended.status, ended.raw)
	assert.Equal(t, kindActivationSuperseded, ended.kind())

	// Control: a new generation under the recovered lineage is a new activation, anchored on the ended one.
	fresh := e.activate(t, recovered, "request-fresh", strings.Repeat("e", 32), "1")
	assert.Equal(t, http.StatusCreated, fresh.status, fresh.raw)
	assert.Equal(t, firstActivation, e.garam.calls[len(e.garam.calls)-1].ReplacesActivationID)
}

func TestActivate_ControllerProvedUnderAnotherEpochRefused(t *testing.T) {
	e := newEnv(t)
	e.garam.set(func(g *garam) { g.proveEpoch = "8" })
	refused := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, kindEpochSuperseded, refused.kind())
	assert.Equal(t, 0, e.garam.callCount())

	// Control: the controller proved under the placement's epoch.
	e.garam.set(func(g *garam) { g.proveEpoch = "" })
	assert.Equal(t, http.StatusCreated, e.activate(t, e.adapter, requestID, generation, "1").status)
}

func TestActivate_AnActivationOfAnotherBindingIsNotPassedOn(t *testing.T) {
	e := newEnv(t)
	e.garam.set(func(g *garam) { g.wrongGRN = "grn:acme:default:agent:other" })
	refused := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusInternalServerError, refused.status, refused.raw)
	assert.NotContains(t, refused.raw, "token-", "a token of another binding was handed out")

	// Control: garam's answer naming this agent is passed on.
	e.garam.set(func(g *garam) { g.wrongGRN = "" })
	assert.Equal(t, http.StatusOK, e.activate(t, e.adapter, requestID, generation, "1").status)
}

// TestActivate_ConcurrentActivationsAreSerialized has two generations activated at once: one at a
// time, the second reads the first as its anchor, so both are activated. Unserialized, both would
// name no anchor and garam would refuse one.
func TestActivate_ConcurrentActivationsAreSerialized(t *testing.T) {
	e := newEnv(t)
	e.garam.set(func(g *garam) { g.delay = 50 * time.Millisecond })
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i, gen := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32)} {
		wg.Go(func() { statuses[i] = e.activate(t, e.adapter, fmt.Sprintf("request-%d", i), gen, "1").status })
	}
	wg.Wait()
	assert.Equal(t, []int{http.StatusCreated, http.StatusCreated}, statuses)
	anchors := []string{e.garam.calls[0].ReplacesActivationID, e.garam.calls[1].ReplacesActivationID}
	assert.ElementsMatch(t, []string{"", firstActivation}, anchors)
}
