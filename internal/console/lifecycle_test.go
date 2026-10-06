package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// lifecycle is the test double for garam's recovery and deactivation: it records each call, and
// answers with the error the test set, or else as garam answers a decided call.
type lifecycle struct {
	mu            sync.Mutex
	recoverErr    error
	deactivateErr error
	recovered     []recovered
	deactivated   []string
}

// recovered is one recovery sent: the handoff and the body, exactly as sent.
type recovered struct {
	handoff console.Authority
	body    []byte
}

func (l *lifecycle) Recover(_ context.Context, _ string, handoff console.Authority, body []byte) (definition.RecoveredCredential, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recovered = append(l.recovered, recovered{handoff: handoff, body: bytes.Clone(body)})
	if l.recoverErr != nil {
		return definition.RecoveredCredential{}, l.recoverErr
	}
	return definition.RecoveredCredential{Lineage: "lineage-2", CertificatePEM: "recovered certificate"}, nil
}

func (l *lifecycle) Deactivate(_ context.Context, _, activationID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deactivated = append(l.deactivated, activationID)
	return l.deactivateErr
}

func (l *lifecycle) set(recoverErr, deactivateErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recoverErr, l.deactivateErr = recoverErr, deactivateErr
}

func (l *lifecycle) calls() ([]recovered, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]recovered(nil), l.recovered...), append([]string(nil), l.deactivated...)
}

// lifecycleAnswer is a recovery's or a stop's answer, refusals included.
type lifecycleAnswer struct {
	status int
	body   map[string]any
}

func (a lifecycleAnswer) kind() string {
	kind, _ := a.body["kind"].(string)
	return kind
}

// lifecycleCall sends body under authority to the agent's route, POST unless body is nil.
func (e *env) lifecycleCall(t *testing.T, route string, authority console.Authority, body []byte) lifecycleAnswer {
	t.Helper()
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, e.url+"/v1/orgs/"+org+"/agents/"+agent+route, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+string(authority))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return lifecycleAnswer{status: resp.StatusCode, body: out}
}

// recoverAuthority is an agent:recover authority for body under requestID.
func (e *env) recoverAuthority(requestID string, body []byte) console.Authority {
	return e.authorize(requestID, body, func(b *console.Binding) { b.Operation = console.OperationRecover })
}

// openBody opens recovery recoveryID under requestID.
func openBody(requestID, recoveryID string) []byte {
	return []byte(`{"requestId":"` + requestID + `","recoveryRequestId":"` + recoveryID + `"}`)
}

// prepare stores k8s's certificate request for recovery recoveryID, as its controller would, and
// returns the prepared body.
func (e *env) prepare(t *testing.T, recoveryID string) []byte {
	t.Helper()
	r, err := e.definitions.PrepareRecovery(context.Background(), definition.InitialCertificateInput{
		Agent: agent, Controller: controllerGRN,
		Request: definition.CertificateRequest{RequestID: recoveryID, Epoch: "1", CSRPEM: "csr of " + recoveryID},
	})
	require.NoError(t, err)
	return r.Body
}

// readRecovery reads the agent's recovery as an administrator does, and returns its prepared body.
func (e *env) readRecovery(t *testing.T) lifecycleAnswer {
	t.Helper()
	target := "/v1/orgs/" + org + "/agents/" + agent + "/recovery"
	read := e.authorize("read", nil, func(b *console.Binding) {
		b.Operation, b.RequestTarget = console.OperationExecutionRead, target
	})
	return e.lifecycleCall(t, "/recovery", read, nil)
}

func TestRecovery_FinalizeSendsGaramThePreparedBytesUnderTheHandoff(t *testing.T) {
	e := newEnv(t)
	opened := e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-1", openBody("open-1", "rec-1")), openBody("open-1", "rec-1"))
	require.Equal(t, http.StatusCreated, opened.status, opened.body)
	assert.Equal(t, "requested", opened.body["stage"])
	assert.Nil(t, opened.body["body"])

	prepared := e.prepare(t, "rec-1")
	read := e.readRecovery(t)
	require.Equal(t, http.StatusOK, read.status, read.body)
	assert.Equal(t, "prepared", read.body["stage"])
	require.Equal(t, string(prepared), read.body["body"], "the read does not carry the bytes the handoff is minted over")

	handoff := e.recoverAuthority("rec-1", prepared)
	finalized := e.lifecycleCall(t, "/recovery/finalize", handoff, prepared)
	require.Equal(t, http.StatusCreated, finalized.status, finalized.body)
	assert.Equal(t, "finalized", finalized.body["stage"])
	assert.Equal(t, "recovered certificate", finalized.body["certificatePem"])
	sent, _ := e.lifecycle.calls()
	require.Len(t, sent, 1)
	assert.Equal(t, prepared, sent[0].body, "garam was not sent the prepared bytes")
	assert.Equal(t, handoff, sent[0].handoff, "garam was not sent the handoff the finalize presented")

	// A repeat is answered from the store: garam is not asked twice.
	again := e.lifecycleCall(t, "/recovery/finalize", handoff, prepared)
	require.Equal(t, http.StatusOK, again.status, again.body)
	sent, _ = e.lifecycle.calls()
	assert.Len(t, sent, 1)
}

func TestRecovery_FinalizeRefusesAnythingButThePreparedRequest(t *testing.T) {
	e := newEnv(t)
	open := openBody("open-1", "rec-1")
	require.Equal(t, http.StatusCreated, e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-1", open), open).status)
	early := []byte(`{"requestId":"rec-1","epoch":"1","certificateRequestPem":"csr of rec-1"}`)

	before := e.lifecycleCall(t, "/recovery/finalize", e.recoverAuthority("rec-1", early), early)
	assert.Equal(t, http.StatusConflict, before.status)
	assert.Equal(t, "recovery_stage", before.kind(), "a recovery was finalized before it was prepared")

	prepared := e.prepare(t, "rec-1")
	reordered := []byte(`{"epoch":"1","requestId":"rec-1","certificateRequestPem":"csr of rec-1"}`)
	require.NotEqual(t, prepared, reordered)
	mismatch := e.lifecycleCall(t, "/recovery/finalize", e.recoverAuthority("rec-1", reordered), reordered)
	assert.Equal(t, http.StatusConflict, mismatch.status)
	assert.Equal(t, "recovery_mismatch", mismatch.kind(), "a body other than the prepared bytes was finalized")

	other := e.lifecycleCall(t, "/recovery/finalize", e.recoverAuthority("open-1", prepared), prepared)
	assert.Equal(t, http.StatusForbidden, other.status, "an authority of another request id finalized it")

	configure := e.lifecycleCall(t, "/recovery/finalize", e.authorize("rec-1", prepared, nil), prepared)
	assert.Equal(t, http.StatusForbidden, configure.status, "an agent:configure authority finalized a recovery")

	sent, _ := e.lifecycle.calls()
	assert.Empty(t, sent, "garam was asked for a refused finalize")

	// Control: the prepared bytes under their own handoff are finalized.
	accepted := e.lifecycleCall(t, "/recovery/finalize", e.recoverAuthority("rec-1", prepared), prepared)
	assert.Equal(t, http.StatusCreated, accepted.status, accepted.body)
}

func TestRecovery_GaramsRefusalAndUndecidedLeaveItPreparedForTheSameHandoff(t *testing.T) {
	e := newEnv(t)
	open := openBody("open-1", "rec-1")
	require.Equal(t, http.StatusCreated, e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-1", open), open).status)
	prepared := e.prepare(t, "rec-1")
	handoff := e.recoverAuthority("rec-1", prepared)

	e.lifecycle.set(&console.LifecycleRefusal{Status: http.StatusConflict, Kind: "conflict", Message: "digest"}, nil)
	refused := e.lifecycleCall(t, "/recovery/finalize", handoff, prepared)
	assert.Equal(t, http.StatusConflict, refused.status)
	assert.Equal(t, "conflict", refused.kind(), "garam's refusal was not answered under its own kind")

	e.lifecycle.set(console.ErrLifecycleUndecided, nil)
	undecided := e.lifecycleCall(t, "/recovery/finalize", handoff, prepared)
	assert.Equal(t, http.StatusServiceUnavailable, undecided.status)
	assert.Equal(t, "prepared", e.readRecovery(t).body["stage"], "an undecided recovery did not stay prepared")

	// Control: the same body under the same handoff is sent again and recorded on garam's answer.
	e.lifecycle.set(nil, nil)
	finalized := e.lifecycleCall(t, "/recovery/finalize", handoff, prepared)
	assert.Equal(t, http.StatusCreated, finalized.status, finalized.body)
	sent, _ := e.lifecycle.calls()
	require.Len(t, sent, 3)
	assert.Equal(t, sent[0], sent[2], "the retry was not the same request")
}

func TestRecovery_OpenRefusals(t *testing.T) {
	e := newEnv(t)
	open := openBody("open-1", "rec-1")

	configure := e.lifecycleCall(t, "/recovery", e.authorize("open-1", open, nil), open)
	assert.Equal(t, http.StatusForbidden, configure.status, "an agent:configure authority opened a recovery")

	invalid := openBody("open-1", "rec 1")
	assert.Equal(t, http.StatusBadRequest, e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-1", invalid), invalid).status)

	// Control: the agent:recover authority bound to the body opens it.
	require.Equal(t, http.StatusCreated, e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-1", open), open).status)

	second := openBody("open-2", "rec-2")
	refused := e.lifecycleCall(t, "/recovery", e.recoverAuthority("open-2", second), second)
	assert.Equal(t, http.StatusConflict, refused.status)
	assert.Equal(t, "recovery_open", refused.kind())
}

func TestStop_StopsDeactivatesAndStartAdmitsTheNextActivation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	activation := definition.ActivationRequest{
		RequestID: "a1", Epoch: "1", Generation: strings.Repeat("a", 32), ConfigRevision: 1, PlacementPodUID: "pod-a",
	}
	_, err := e.definitions.PrepareActivation(ctx, agent, activation)
	require.NoError(t, err)
	require.NoError(t, e.definitions.RecordActivation(ctx, agent, "a1", "activation-a"))
	stop := []byte(`{"requestId":"stop-1"}`)

	e.lifecycle.set(nil, console.ErrLifecycleUndecided)
	undecided := e.lifecycleCall(t, "/stop", e.authorize("stop-1", stop, nil), stop)
	assert.Equal(t, http.StatusServiceUnavailable, undecided.status)
	_, err = e.definitions.PrepareActivation(ctx, agent, activation)
	require.ErrorIs(t, err, definition.ErrAgentStopped, "the stop was not recorded before garam was asked")

	// The repeat finishes the deactivation on garam's answer.
	e.lifecycle.set(nil, nil)
	stopped := e.lifecycleCall(t, "/stop", e.authorize("stop-1", stop, nil), stop)
	require.Equal(t, http.StatusOK, stopped.status, stopped.body)
	assert.Equal(t, true, stopped.body["stopped"])
	assert.Equal(t, true, stopped.body["deactivated"])
	_, deactivated := e.lifecycle.calls()
	assert.Equal(t, []string{"activation-a", "activation-a"}, deactivated)

	// Once deactivated, a repeat does not ask garam again.
	require.Equal(t, http.StatusOK, e.lifecycleCall(t, "/stop", e.authorize("stop-1", stop, nil), stop).status)
	_, deactivated = e.lifecycle.calls()
	assert.Len(t, deactivated, 2)

	start := []byte(`{"requestId":"start-1"}`)
	started := e.lifecycleCall(t, "/start", e.authorize("start-1", start, nil), start)
	require.Equal(t, http.StatusOK, started.status, started.body)
	assert.Equal(t, false, started.body["stopped"])
	_, err = e.definitions.PrepareActivation(ctx, agent, definition.ActivationRequest{
		RequestID: "a2", Epoch: "1", Generation: strings.Repeat("b", 32), ConfigRevision: 1, PlacementPodUID: "pod-a",
	})
	require.NoError(t, err)
}

func TestStop_Refusals(t *testing.T) {
	e := newEnv(t)
	start := []byte(`{"requestId":"start-1"}`)
	notStopped := e.lifecycleCall(t, "/start", e.authorize("start-1", start, nil), start)
	assert.Equal(t, http.StatusConflict, notStopped.status)
	assert.Equal(t, "agent_not_stopped", notStopped.kind())

	stop := []byte(`{"requestId":"stop-1"}`)
	recover := e.lifecycleCall(t, "/stop", e.recoverAuthority("stop-1", stop), stop)
	assert.Equal(t, http.StatusForbidden, recover.status, "an agent:recover authority stopped the agent")

	// Control: agent:configure stops it, and an agent with no activation has nothing to deactivate.
	stopped := e.lifecycleCall(t, "/stop", e.authorize("stop-1", stop, nil), stop)
	require.Equal(t, http.StatusOK, stopped.status, stopped.body)
	assert.Nil(t, stopped.body["activationId"])
	_, deactivated := e.lifecycle.calls()
	assert.Empty(t, deactivated)

	again := []byte(`{"requestId":"stop-2"}`)
	twice := e.lifecycleCall(t, "/stop", e.authorize("stop-2", again, nil), again)
	assert.Equal(t, http.StatusConflict, twice.status)
	assert.Equal(t, "agent_stopped", twice.kind())
}
