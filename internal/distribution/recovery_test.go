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

// stopOperation is the operation a console stop is bound to.
const stopOperation = "agent:configure"

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

// recoveryBody is a controller's request for its agent's recovery recoveryID at epoch.
func recoveryBody(recoveryID, epoch string) string {
	return `{"requestId":"` + recoveryID + `","epoch":"` + epoch + `"}`
}

// prepareRecovery posts body for agent's recovery with client, and returns the status and the answer.
func (e *env) prepareRecovery(t *testing.T, agent, body string) (int, map[string]any) {
	t.Helper()
	resp, err := e.withToken.Post(e.server.URL+"/v1/operators/self/agents/"+agent+"/recovery-requests",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

func TestPrepareRecovery_StoresTheRequestAndAnswersTheNewLineage(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.openRecovery(t, agentA)
	body := recoveryBody("rec-1", epoch)

	status, out := e.prepareRecovery(t, agentA, body)
	require.Equal(t, http.StatusAccepted, status, out)
	assert.Equal(t, "prepared", out["stage"])
	assert.Nil(t, out["lineage"])
	status, _ = e.prepareRecovery(t, agentA, body)
	assert.Equal(t, http.StatusAccepted, status, "an identical repeat was not answered as stored")
	stored, err := e.definitions.RecoveryOf(ctx, orgID, agentA)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(stored.Body), "the request garam is to be sent is not the controller's")

	_, err = e.definitions.FinalizeRecovery(ctx, agentA, "rec-1", definition.RecoveredCredential{Lineage: "lineage-2"})
	require.NoError(t, err)
	status, out = e.prepareRecovery(t, agentA, body)
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "lineage-2", out["lineage"])
	assert.NotContains(t, out, "certificatePem")
}

func TestPrepareRecovery_Refusals(t *testing.T) {
	e := newEnv(t)
	e.openRecovery(t, agentA)

	status, _ := e.prepareRecovery(t, agentA, recoveryBody("rec-unknown", epoch))
	assert.Equal(t, http.StatusNotFound, status, "a request for no open recovery was stored")
	status, _ = e.prepareRecovery(t, agentC, recoveryBody("rec-1", epoch))
	assert.Equal(t, http.StatusForbidden, status, "another controller's agent was prepared here")
	status, out := e.prepareRecovery(t, agentA, recoveryBody("rec-1", "6"))
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "epoch_superseded", out["kind"])
	status, _ = e.prepareRecovery(t, agentA,
		`{"requestId":"rec-1","epoch":"`+epoch+`","certificateRequestPem":"-----BEGIN CERTIFICATE REQUEST-----"}`)
	assert.Equal(t, http.StatusBadRequest, status, "a certificate request was taken under execution-fence.v2")

	// Control: the open recovery's own request is stored.
	status, out = e.prepareRecovery(t, agentA, recoveryBody("rec-1", epoch))
	require.Equal(t, http.StatusAccepted, status, out)
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
		Binding: definition.Binding{Actor: "admin", Operation: stopOperation, Target: agentB},
	})
	require.NoError(t, err)

	agents = e.rawAgents(t)
	assert.Equal(t, map[string]any{"requestId": "rec-1", keyEpoch: epoch}, agents[agentA]["recovery"])
	assert.NotContains(t, agents[agentA], "stopped")
	assert.Equal(t, true, agents[agentB]["stopped"])
	assert.NotContains(t, agents[agentB], "recovery")
}
