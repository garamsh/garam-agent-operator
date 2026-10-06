//go:build e2e

package control_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendTargeted sends method on the binary's path under an authority real garam minted for
// operation on target, bound to that exact request target and body, and returns the status and
// the answer.
func sendTargeted(t *testing.T, method, path, operation, target, requestID string, body []byte,
	boundPath string) (int, map[string]any) {
	t.Helper()
	digest := sha256.Sum256(body)
	authority, _, err := real.mintTargeted(operation, target, requestID, hex.EncodeToString(digest[:]), boundPath)
	require.NoError(t, err)
	req, err := http.NewRequest(method, attachedURL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var answer map[string]any
	require.NoError(t, json.Unmarshal(raw, &answer), string(raw))
	return resp.StatusCode, answer
}

// TestTemplates_PublishThenReadThroughGaram publishes a template version through the binary under
// a real agent-template:publish authority, reads it back under real agent-template:read
// authorities, and finds another organization's template of the same store not found.
func TestTemplates_PublishThenReadThroughGaram(t *testing.T) {
	org := real.orgID
	profile := publishProfile(t, org)
	template := name(t, "template")
	base := "/v1/orgs/" + org

	// The publish body is configure's without the expected revision, as the console sends it.
	var in configureRequest
	require.NoError(t, json.Unmarshal(configureBody(name(t, "publish"), profile, "Answers web questions.", 1), &in))
	in.Configuration.Model.APIKeyRef = "model-keys/web"
	body, err := json.Marshal(struct {
		RequestID     string `json:"requestId"`
		Profile       any    `json:"profile"`
		Configuration any    `json:"configuration"`
	}{in.RequestID, in.Profile, in.Configuration})
	require.NoError(t, err)

	publish := base + "/templates/" + template + "/versions"
	status, published := sendTargeted(t, http.MethodPost, publish, "agent-template:publish", real.orgGRN,
		in.RequestID, body, publish)
	require.Equal(t, http.StatusCreated, status, published)
	assert.Equal(t, template, published["name"])
	assert.InDelta(t, 1, published["version"], 0)

	list := base + "/templates"
	status, listed := sendTargeted(t, http.MethodGet, list, "agent-template:read", real.orgGRN,
		name(t, "list"), nil, list)
	require.Equal(t, http.StatusOK, status, listed)
	listedTemplate := map[string]any{"version": float64(1), "profile": map[string]any{"version": float64(1)}}
	listedTemplate["name"], listedTemplate["profile"].(map[string]any)["name"] = template, profile
	assert.Contains(t, listed["templates"], listedTemplate)

	version := base + "/templates/" + template + "/versions/1"
	status, read := sendTargeted(t, http.MethodGet, version, "agent-template:read", real.orgGRN,
		name(t, "read"), nil, version)
	require.Equal(t, http.StatusOK, status, read)
	configuration := read["configuration"].(map[string]any)
	assert.Equal(t, "model-keys/web", configuration["model"].(map[string]any)["apiKeyRef"])
	assert.Equal(t, "Answers web questions.", configuration["ego"])

	// A read authority garam bound to another request target is refused before anything is read.
	status, _ = sendTargeted(t, http.MethodGet, version, "agent-template:read", real.orgGRN,
		name(t, "other"), nil, list)
	assert.Equal(t, http.StatusForbidden, status)

	// Another organization's template in the same store is not found through this one's path, as a
	// name nobody published is not.
	other := name(t, "organization")
	foreign := name(t, "template")
	require.NoError(t, insertTemplate(t, other, foreign, publishProfile(t, other)))
	missing := base + "/templates/" + foreign + "/versions/1"
	status, _ = sendTargeted(t, http.MethodGet, missing, "agent-template:read", real.orgGRN,
		name(t, "foreign"), nil, missing)
	assert.Equal(t, http.StatusNotFound, status)
}
