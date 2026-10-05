//go:build e2e

package control_test

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyAgent is an agent garam's legacy route creates for the controller: assigned to it, with no
// definition, so its cutover source is empty values under its assignee.
func legacyAgent(t *testing.T) string {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      real.machine.Transport.(*http.Transport).TLSClientConfig.RootCAs,
		Certificates: []tls.Certificate{real.controller},
	}}}
	var created struct {
		GRN string `json:"grn"`
	}
	require.NoError(t, real.machineCall(client, "/agents", "", nil, http.StatusCreated, &created))
	require.NotEmpty(t, created.GRN)
	return created.GRN
}

// stageTarget is garam's machine route a stage's reference binds.
func stageTarget(agent, stage string) string {
	target := "/agents/" + url.PathEscape(agent) + "/cutover"
	if stage != "import" {
		target += "/" + stage
	}
	return target
}

// mintFor has garam mint an authority for operation on agent, for exactly body, bound to
// requestTarget where it is not empty.
func mintFor(t *testing.T, operation, agent, requestID, requestTarget string, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(body)
	var minted struct {
		Authority string `json:"authority"`
	}
	require.NoError(t, real.create(http.MethodPost, "/orgs/"+real.orgID+"/operation-authorities", garamContract, struct {
		Audience      string `json:"audience"`
		Operation     string `json:"operation"`
		Target        string `json:"target"`
		RequestID     string `json:"requestId"`
		BodySHA256    string `json:"bodySha256"`
		RequestTarget string `json:"requestTarget,omitempty"`
	}{real.hosted, operation, agent, requestID, hex.EncodeToString(digest[:]), requestTarget}, &minted))
	return minted.Authority
}

// cutoverStage posts body to the console's route for stage, under authority and, for the switch,
// the agent:configure authority beside it.
func cutoverStage(t *testing.T, agent, stage string, body []byte, authority, configure string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, attachedURL+"/v1/orgs/"+real.orgID+"/agents/"+agent+"/cutover/"+stage,
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	if configure != "" {
		req.Header.Set("Garam-Configure-Operation", "Garam-Operation "+configure)
	}
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

// runStage mints the stage's own authority for a fresh request and carries it; the switch's
// agent:configure authority is minted under the configure request id its body names.
func runStage(t *testing.T, agent, stage string, body []byte, requestID string) (int, map[string]any) {
	t.Helper()
	authority := mintFor(t, "agent:cutover", agent, requestID, stageTarget(agent, stage), body)
	configure := ""
	if stage == "switch" {
		configure = mintFor(t, "agent:configure", agent, "configure-"+requestID, "", body)
	}
	return cutoverStage(t, agent, stage, body, authority, configure)
}

// switchOf is a switch under requestID, naming its configure authority's request id.
func switchOf(t *testing.T, requestID string) []byte {
	t.Helper()
	return requestBody(t, struct {
		RequestID          string `json:"requestId"`
		ConfigureRequestID string `json:"configureRequestId"`
	}{requestID, "configure-" + requestID})
}

func requestBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

type stageBody struct {
	RequestID string `json:"requestId"`
}

// importLegacy imports agent under profile, as the cutover's first stage.
func importLegacy(t *testing.T, agent, profile string) (int, map[string]any) {
	t.Helper()
	requestID := name(t, "import")
	body := requestBody(t, map[string]any{
		"requestId": requestID, "profile": map[string]any{"name": profile, "version": 1}, "dispositions": map[string]string{},
	})
	return runStage(t, agent, "import", body, requestID)
}

// releasedCutovers is the controller's feed, as its revision and origin per agent.
func releasedCutovers(t *testing.T) map[string]string {
	t.Helper()
	resp, err := real.feedClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var feed struct {
		Agents []struct {
			Agent, Revision, Origin string
		} `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	out := map[string]string{}
	for _, a := range feed.Agents {
		out[a.Agent] = a.Revision + "/" + a.Origin
	}
	return out
}

func TestCutover_ImportFreezeSwitchReleasesALegacyAgentAgainstGaram(t *testing.T) {
	agent := legacyAgent(t)
	profile := publishProfile(t)

	status, imported := importLegacy(t, agent, profile)
	require.Equal(t, http.StatusCreated, status, imported)
	assert.Equal(t, "imported", imported["stage"])
	const inactive = "SELECT count(*) FROM definitions WHERE agent = $1 AND assignment_operator IS NULL"
	assert.Equal(t, 1, count(t, inactive, agent))
	assert.NotContains(t, releasedCutovers(t), agent, "the import was released before the switch")

	freezeID := name(t, "freeze")
	status, frozen := runStage(t, agent, "freeze", requestBody(t, stageBody{freezeID}), freezeID)
	require.Equal(t, http.StatusOK, status, frozen)
	assert.Equal(t, "frozen", frozen["stage"])
	assert.NotContains(t, releasedCutovers(t), agent, "the import was released before the switch")

	// A reference minted for the freeze is refused on the switch, before garam is asked.
	switchID := name(t, "switch")
	refusedID := name(t, "switch")
	refusedBody := switchOf(t, refusedID)
	freezeReference := mintFor(t, "agent:cutover", agent, refusedID, stageTarget(agent, "freeze"), refusedBody)
	status, refused := cutoverStage(t, agent, "switch", refusedBody, freezeReference,
		mintFor(t, "agent:configure", agent, "configure-"+refusedID, "", refusedBody))
	assert.Equal(t, http.StatusForbidden, status, refused)
	assert.Contains(t, refused["message"], "request target")

	// The switch, under its own reference: garam records it, and only then is revision 1 released.
	status, switched := runStage(t, agent, "switch", switchOf(t, switchID), switchID)
	require.Equal(t, http.StatusOK, status, switched)
	assert.Equal(t, "switched", switched["stage"])
	assert.Equal(t, "1/cutover", releasedCutovers(t)[agent])

	// After the switch there is no rollback.
	rollbackID := name(t, "rollback")
	status, refusedRollback := runStage(t, agent, "rollback", requestBody(t, stageBody{rollbackID}), rollbackID)
	assert.Equal(t, http.StatusConflict, status, refusedRollback)
	assert.Equal(t, "reverse_migration_required", refusedRollback["kind"])
	assert.Equal(t, "1/cutover", releasedCutovers(t)[agent])
}

func TestCutover_RollbackFromFrozenAgainstGaram(t *testing.T) {
	agent := legacyAgent(t)
	status, imported := importLegacy(t, agent, publishProfile(t))
	require.Equal(t, http.StatusCreated, status, imported)
	freezeID := name(t, "freeze")
	status, frozen := runStage(t, agent, "freeze", requestBody(t, stageBody{freezeID}), freezeID)
	require.Equal(t, http.StatusOK, status, frozen)

	rollbackID := name(t, "rollback")
	status, rolledBack := runStage(t, agent, "rollback", requestBody(t, stageBody{rollbackID}), rollbackID)
	require.Equal(t, http.StatusOK, status, rolledBack)
	assert.Equal(t, "rolled_back", rolledBack["stage"])
	assert.Equal(t, 0, count(t, "SELECT count(*) FROM cutover_imports WHERE agent = $1", agent))
	assert.Equal(t, 0, count(t, "SELECT count(*) FROM definitions WHERE agent = $1", agent),
		"the import's revision 1 was kept")
	assert.NotContains(t, releasedCutovers(t), agent)
}
