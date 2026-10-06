//go:build e2e

package control_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fenceContract is garam's execution-fence.v1, which its recovery, deactivation and execution
// introspection speak.
const fenceContract = "execution-fence.v1"

// lifecycleRoute is the console route of agent under route.
func lifecycleRoute(agent, route string) string {
	return "/v1/orgs/" + real.orgID + "/agents/" + agent + route
}

// consoleSend sends body under authority to the attached binary's console path, GET where body is
// nil, and returns the status and the answer.
func consoleSend(t *testing.T, path, authority string, body []byte) (int, map[string]any) {
	t.Helper()
	method := http.MethodPost
	if body == nil {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, attachedURL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

// mint has the real garam mint operation on agent for requestID and body.
func mint(t *testing.T, operation, agent, requestID string, body []byte) string {
	t.Helper()
	authority, _, err := real.mintAuthority(operation, agent, requestID, sha(string(body)))
	require.NoError(t, err)
	return authority
}

// readRecovery reads agent's recovery as an administrator does, under agent:execution-read bound to
// the route.
func readRecovery(t *testing.T, agent string) map[string]any {
	t.Helper()
	path := lifecycleRoute(agent, "/recovery")
	read, _, err := real.mintTargeted("agent:execution-read", agent, name(t, "read"), sha(""), path)
	require.NoError(t, err)
	status, out := consoleSend(t, path, read, nil)
	require.Equal(t, http.StatusOK, status, out)
	return out
}

// desiredOf is agent's entry in its controller's feed.
func desiredOf(t *testing.T, agent string) map[string]any {
	t.Helper()
	resp, err := real.feedClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var feed struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	for _, a := range feed.Agents {
		if a["agent"] == agent {
			return a
		}
	}
	require.Failf(t, "not released", "the feed does not carry %s", agent)
	return nil
}

// prepareAs posts the controller's certificate request for agent's recovery, as its manager will.
func prepareAs(t *testing.T, agent, body string) (int, map[string]any) {
	t.Helper()
	resp, err := real.feedClient().Post(attachedURL+"/v1/operators/self/agents/"+agent+"/recovery-requests",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

// openAndPrepare opens a recovery of a from the console under a fresh agent:recover authority, has
// its controller prepare it over a key generated here, and returns the recovery's identifier, the
// prepared bytes as the read answers them, and the pair of that key with a recovered certificate.
func openAndPrepare(t *testing.T, a placedAgent) (recoveryID string, prepared []byte, pairFor func(string) tls.Certificate) {
	t.Helper()
	recoveryID, openID := name(t, "recovery"), name(t, "open")
	open := requestBody(t, struct {
		RequestID         string `json:"requestId"`
		RecoveryRequestID string `json:"recoveryRequestId"`
	}{openID, recoveryID})
	status, opened := consoleSend(t, lifecycleRoute(a.grn, "/recovery"), mint(t, "agent:recover", a.grn, openID, open), open)
	require.Equal(t, http.StatusCreated, status, opened)
	assert.Equal(t, "requested", opened["stage"])
	assert.Equal(t, map[string]any{"requestId": recoveryID, "epoch": a.epoch}, desiredOf(t, a.grn)["recovery"],
		"the feed does not tell the controller to prepare the recovery")

	key, csr := certificateRequestPEM(t)
	status, out := prepareAs(t, a.grn, certificateRequestBody(recoveryID, a.epoch, csr))
	require.Equal(t, http.StatusAccepted, status, out)
	read := readRecovery(t, a.grn)
	require.Equal(t, "prepared", read["stage"])
	body, _ := read["body"].(string)
	require.NotEmpty(t, body)
	require.Equal(t, sha(body), read["bodySha256"])
	return recoveryID, []byte(body), func(certificatePEM string) tls.Certificate {
		return tls.Certificate{Certificate: [][]byte{parsePEM(t, certificatePEM).Raw}, PrivateKey: key}
	}
}

func TestRecovery_AgainstGaram(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("a", 32)
	first := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, generation))
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, generation)).status)

	// An agent:configure authority opens no recovery.
	openID := name(t, "open")
	open := requestBody(t, struct {
		RequestID         string `json:"requestId"`
		RecoveryRequestID string `json:"recoveryRequestId"`
	}{openID, name(t, "recovery")})
	status, refused := consoleSend(t, lifecycleRoute(a.grn, "/recovery"), mint(t, "agent:configure", a.grn, openID, open), open)
	assert.Equal(t, http.StatusForbidden, status, refused)

	// Control: an agent:recover authority opens it, and the controller prepares it.
	recoveryID, prepared, pairFor := openAndPrepare(t, a)
	var fields map[string]string
	require.NoError(t, json.Unmarshal(prepared, &fields))

	// A handoff minted over other bytes than the prepared ones is refused before garam is asked: the
	// same request, re-encoded. garam binds one digest to a request id, so it is another agent's
	// recovery that is refused; this agent's finalize below is the control.
	b := placeAgent(t)
	otherID, otherPrepared, _ := openAndPrepare(t, b)
	var otherFields map[string]string
	require.NoError(t, json.Unmarshal(otherPrepared, &otherFields))
	reencoded, err := json.MarshalIndent(otherFields, "", " ")
	require.NoError(t, err)
	status, mismatch := consoleSend(t, lifecycleRoute(b.grn, "/recovery/finalize"),
		mint(t, "agent:recover", b.grn, otherID, reencoded), reencoded)
	assert.Equal(t, http.StatusConflict, status, mismatch)
	assert.Equal(t, "recovery_mismatch", mismatch["kind"])
	assert.Equal(t, "prepared", readRecovery(t, b.grn)["stage"])

	// A restart between prepared and finalize: the stored request is finalized after it.
	restartAttached(t)
	status, finalized := consoleSend(t, lifecycleRoute(a.grn, "/recovery/finalize"),
		mint(t, "agent:recover", a.grn, recoveryID, prepared), prepared)
	require.Equal(t, http.StatusCreated, status, finalized)
	assert.Equal(t, "finalized", finalized["stage"])
	certificatePEM, _ := finalized["certificatePem"].(string)
	require.NotEmpty(t, certificatePEM)
	assert.NotContains(t, desiredOf(t, a.grn), "recovery", "a finalized recovery is still released for preparing")

	// The controller's repeat is answered the recovered certificate, over the key it holds.
	status, fetched := prepareAs(t, a.grn, certificateRequestBody(recoveryID, a.epoch, fields["certificateRequestPem"]))
	require.Equal(t, http.StatusOK, status, fetched)
	assert.Equal(t, certificatePEM, fetched["certificatePem"])
	recovered := pairFor(certificatePEM)

	// garam fenced the old lineage and ended its activation; the recovered leaf activates the next
	// generation.
	old := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, strings.Repeat("b", 32)))
	assert.Equal(t, http.StatusForbidden, old.status, old.raw)
	assert.Equal(t, "credential_fenced", old.body["kind"])
	next := postAgent(t, a, recovered, "activations",
		activationOf(name(t, "activation"), a.epoch, strings.Repeat("c", 32)))
	assert.Equal(t, http.StatusCreated, next.status, next.raw)

	// The recovery's identifier is not opened again.
	reopenID := name(t, "open")
	reopen := requestBody(t, struct {
		RequestID         string `json:"requestId"`
		RecoveryRequestID string `json:"recoveryRequestId"`
	}{reopenID, recoveryID})
	status, reused := consoleSend(t, lifecycleRoute(a.grn, "/recovery"), mint(t, "agent:recover", a.grn, reopenID, reopen), reopen)
	assert.Equal(t, http.StatusConflict, status, reused)
}

// TestRecovery_ResumesWhereGaramAnsweredAndControlDidNotRecord is the restart between garam's
// recovery committing and the control service recording it: the finalize is sent again under a
// handoff minted again for the same request, garam answers the certificate it issued, and that is
// what control records.
func TestRecovery_ResumesWhereGaramAnsweredAndControlDidNotRecord(t *testing.T) {
	a := placeAgent(t)
	recoveryID, prepared, _ := openAndPrepare(t, a)

	// garam recovered under the handoff, and the control service stopped before it recorded it.
	req, err := http.NewRequest(http.MethodPost, real.machineURL+"/agents/"+a.grn+"/credential-recovery",
		bytes.NewReader(prepared))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Garam-Contract-Version", fenceContract)
	req.Header.Set("Authorization", "Garam-Operation "+mint(t, "agent:recover", a.grn, recoveryID, prepared))
	resp, err := real.hostedTLS.Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	var issued struct{ CertificatePem string }
	require.NoError(t, json.Unmarshal(raw, &issued))
	restartAttached(t)
	require.Equal(t, "prepared", readRecovery(t, a.grn)["stage"])

	status, finalized := consoleSend(t, lifecycleRoute(a.grn, "/recovery/finalize"),
		mint(t, "agent:recover", a.grn, recoveryID, prepared), prepared)
	require.Equal(t, http.StatusCreated, status, finalized)
	assert.Equal(t, issued.CertificatePem, finalized["certificatePem"],
		"control recorded another certificate than the one garam issued")
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM recoveries WHERE agent = $1 AND stage = 'finalized'", a.grn))
}

// stopBody is a stop's or a start's body under requestID.
func stopBody(t *testing.T, requestID string) []byte {
	return requestBody(t, struct {
		RequestID string `json:"requestId"`
	}{requestID})
}

// introspectFence asks garam which activation of agent is current, with the leaf pair presents.
func introspectFence(t *testing.T, a placedAgent, leaf []byte, generation string) *string {
	t.Helper()
	var out struct {
		ActivationID *string `json:"activationId"`
	}
	require.NoError(t, real.machineCall(real.hostedTLS, "/agents/"+a.grn+"/execution/introspection", fenceContract,
		map[string]any{"certificatePem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf})),
			"generation": generation}, http.StatusOK, &out))
	return out.ActivationID
}

// activated places a, activates one generation and has its runtime report revision 1 serving, so a
// later activation needs no reference; it returns the activation and its generation.
func activated(t *testing.T, a placedAgent, generation string) string {
	t.Helper()
	first := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, generation))
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, generation)).status)
	return activation
}

func TestStop_AgainstGaram(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("d", 32)
	activation := activated(t, a, generation)
	require.NotNil(t, introspectFence(t, a, a.pair.Certificate[0], generation))
	require.Equal(t, activation, *introspectFence(t, a, a.pair.Certificate[0], generation))

	// An agent:recover authority stops nothing. garam binds a request id to one operation, so the
	// refused request has its own.
	refusedID := name(t, "stop")
	refusedBody := stopBody(t, refusedID)
	status, refused := consoleSend(t, lifecycleRoute(a.grn, "/stop"),
		mint(t, "agent:recover", a.grn, refusedID, refusedBody), refusedBody)
	assert.Equal(t, http.StatusForbidden, status, refused)

	// Control: agent:configure stops it, and garam ends its activation.
	stopID := name(t, "stop")
	body := stopBody(t, stopID)
	status, stopped := consoleSend(t, lifecycleRoute(a.grn, "/stop"), mint(t, "agent:configure", a.grn, stopID, body), body)
	require.Equal(t, http.StatusOK, status, stopped)
	assert.Equal(t, true, stopped["stopped"])
	assert.Equal(t, true, stopped["deactivated"])
	assert.Equal(t, activation, stopped["activationId"])
	assert.Nil(t, introspectFence(t, a, a.pair.Certificate[0], generation), "garam still routes to the stopped activation")
	assert.Equal(t, true, desiredOf(t, a.grn)["stopped"], "the feed does not keep the runtime stopped")

	// A new placement is registered, and nothing is activated on it.
	a.token = "token pod-2"
	mustPlace(t, a.grn, placementOf(a.epoch, "pod-2", "pod-1"), http.StatusCreated)
	replacement := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, strings.Repeat("e", 32)))
	assert.Equal(t, http.StatusForbidden, replacement.status, replacement.raw)
	assert.Equal(t, "placement_not_current", replacement.body["kind"], "a replacement was activated while stopped")

	againID := name(t, "stop")
	again := stopBody(t, againID)
	status, twice := consoleSend(t, lifecycleRoute(a.grn, "/stop"), mint(t, "agent:configure", a.grn, againID, again), again)
	assert.Equal(t, http.StatusConflict, status, twice)
	assert.Equal(t, "agent_stopped", twice["kind"])

	// The start ends the stop, and the next generation on the new placement is activated.
	startID := name(t, "start")
	start := stopBody(t, startID)
	status, started := consoleSend(t, lifecycleRoute(a.grn, "/start"), mint(t, "agent:configure", a.grn, startID, start), start)
	require.Equal(t, http.StatusOK, status, started)
	assert.Equal(t, false, started["stopped"])
	assert.NotContains(t, desiredOf(t, a.grn), "stopped")
	next := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, strings.Repeat("f", 32)))
	assert.Equal(t, http.StatusCreated, next.status, next.raw)
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM stops WHERE agent = $1 AND started_at IS NOT NULL", a.grn),
		"the ended stop was not kept")
}

// TestStop_ARestartAroundTheDeactivationResumesOnGaramsAnswer is a restart of the control service
// before it asked garam to deactivate, and one after garam answered and before it was recorded:
// each repeat of the stop request asks garam, and records the deactivation on its answer alone.
func TestStop_ARestartAroundTheDeactivationResumesOnGaramsAnswer(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("7", 32)
	activation := activated(t, a, generation)
	stopID := name(t, "stop")
	body := stopBody(t, stopID)
	// One authority for the request throughout: a token minted again for the same binding ends the
	// one before it.
	authority, ref, err := real.mintAuthority("agent:configure", a.grn, stopID, sha(string(body)))
	require.NoError(t, err)

	// The stop recorded, and the control service stopped before it asked garam.
	_, err = pool.Exec(t.Context(), `INSERT INTO stops (organization, request_id, agent, actor, operation, target,
    body_sha256, operation_ref, assignment_operator, assignment_epoch, activation_id)
SELECT $1, $2, $3, actor, 'agent:configure', $3, $4, $5, $6, $7, $8 FROM creations WHERE agent = $3`,
		real.orgID, stopID, a.grn, sha(string(body)), ref, real.controllerGRN, a.epoch, activation)
	require.NoError(t, err)
	restartAttached(t)
	require.Equal(t, activation, *introspectFence(t, a, a.pair.Certificate[0], generation))
	refused := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, strings.Repeat("8", 32)))
	assert.Equal(t, http.StatusForbidden, refused.status, "an activation was admitted between the stop and its deactivation")

	status, stopped := consoleSend(t, lifecycleRoute(a.grn, "/stop"), authority, body)
	require.Equal(t, http.StatusOK, status, stopped)
	assert.Equal(t, true, stopped["deactivated"])
	assert.Nil(t, introspectFence(t, a, a.pair.Certificate[0], generation), "the resumed stop did not deactivate")

	// garam deactivated, and the control service stopped before it recorded it.
	_, err = pool.Exec(t.Context(), "UPDATE stops SET deactivated_at = NULL WHERE agent = $1", a.grn)
	require.NoError(t, err)
	restartAttached(t)
	status, again := consoleSend(t, lifecycleRoute(a.grn, "/stop"), authority, body)
	require.Equal(t, http.StatusOK, status, again)
	assert.Equal(t, true, again["deactivated"])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM stops WHERE agent = $1 AND deactivated_at IS NOT NULL", a.grn))
}
