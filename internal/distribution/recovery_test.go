package distribution_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// openRecovery opens recovery rec-1 of agent from the console, as an agent:recover authority binds it.
func (e *env) openRecovery(t *testing.T, agent string) {
	t.Helper()
	_, _, err := e.definitions.OpenRecovery(context.Background(), definition.OpenRecoveryInput{
		Key: definition.RequestKey{Organization: orgID, RequestID: "open-" + agent},
		Binding: definition.Binding{
			Actor: "admin", Operation: "agent:recover", Target: agent,
			Assignment: definition.Assignment{Operator: controller, Epoch: epoch},
		},
		Agent: definition.GRN(agent), RequestID: "rec-1",
	})
	require.NoError(t, err)
}

// prepareRecovery posts body for agent's recovery with client, and returns the status and the answer.
func (e *env) prepareRecovery(t *testing.T, agent, body string) (int, map[string]any) {
	t.Helper()
	resp, err := e.withCert.Post(e.server.URL+"/v1/operators/self/agents/"+agent+"/recovery-requests",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

func TestPrepareRecovery_StoresTheRequestAndAnswersTheRecoveredCredential(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.openRecovery(t, agentA)
	body := certificateBody("rec-1", epoch, csrPEM(t, p256))

	status, out := e.prepareRecovery(t, agentA, body)
	require.Equal(t, http.StatusAccepted, status, out)
	assert.Equal(t, "prepared", out["stage"])
	assert.Nil(t, out["certificatePem"])
	status, _ = e.prepareRecovery(t, agentA, body)
	assert.Equal(t, http.StatusAccepted, status, "an identical repeat was not answered as stored")

	_, err := e.definitions.FinalizeRecovery(ctx, agentA, "rec-1",
		definition.RecoveredCredential{Lineage: "lineage-2", CertificatePEM: "recovered"})
	require.NoError(t, err)
	status, out = e.prepareRecovery(t, agentA, body)
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "lineage-2", out["lineage"])
	assert.Equal(t, "recovered", out["certificatePem"])
	assert.Equal(t, 0, e.issuer.calls(), "a recovery asked for a first certificate")
}

func TestPrepareRecovery_Refusals(t *testing.T) {
	e := newEnv(t)
	e.openRecovery(t, agentA)
	csr := csrPEM(t, p256)
	accepted := certificateBody("rec-1", epoch, csr)

	status, _ := e.prepareRecovery(t, agentA, certificateBody("rec-unknown", epoch, csr))
	assert.Equal(t, http.StatusNotFound, status, "a certificate request for no open recovery was stored")
	status, _ = e.prepareRecovery(t, agentC, certificateBody("rec-1", epoch, csr))
	assert.Equal(t, http.StatusForbidden, status, "another controller's agent was prepared here")
	status, out := e.prepareRecovery(t, agentA, certificateBody("rec-1", "6", csr))
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "epoch_superseded", out["kind"])

	// Control: the open recovery's own request is stored.
	status, out = e.prepareRecovery(t, agentA, accepted)
	require.Equal(t, http.StatusAccepted, status, out)

	status, out = e.prepareRecovery(t, agentA, certificateBody("rec-1", epoch, csrPEM(t, p256)))
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "request_reused", out["kind"], "another key replaced the prepared request")
}

func TestDesired_CarriesTheOpenRecoveryAndTheStop(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	agents := e.rawAgents(t)
	assert.NotContains(t, agents[agentA], "recovery")
	assert.NotContains(t, agents[agentA], "stopped")

	e.openRecovery(t, agentA)
	_, _, err := e.definitions.Stop(ctx, definition.StopInput{
		Key: definition.RequestKey{Organization: orgID, RequestID: "stop-1"}, Agent: agentB,
		Binding: definition.Binding{Actor: "admin", Operation: "agent:configure", Target: agentB},
	})
	require.NoError(t, err)

	agents = e.rawAgents(t)
	assert.Equal(t, map[string]any{"requestId": "rec-1", "epoch": epoch}, agents[agentA]["recovery"])
	assert.NotContains(t, agents[agentA], "stopped")
	assert.Equal(t, true, agents[agentB]["stopped"])
	assert.NotContains(t, agents[agentB], "recovery")
}
