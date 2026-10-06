//go:build e2e

package control_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureKind posts in under a fresh authority garam mints for it, and answers the status and
// the refusal's kind.
func configureKind(t *testing.T, g garam, in configureRequest) (int, string) {
	t.Helper()
	body := in.body()
	url := fmt.Sprintf("%s/v1/orgs/%s/agents/%s/revisions", attachedURL, g.org(), g.agent())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+g.mint(t, in.RequestID, body))
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Kind string `json:"kind"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out.Kind
}

// releasedEmbedding is the embedding the controller's feed releases for g's agent, nil where it
// names none.
func releasedEmbedding(t *testing.T, g garam) map[string]any {
	t.Helper()
	resp, err := g.controllerClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var feed struct {
		Agents []struct {
			Agent         string `json:"agent"`
			Configuration struct {
				Model map[string]any `json:"model"`
			} `json:"configuration"`
		} `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	for _, a := range feed.Agents {
		if a.Agent == g.agent() {
			embedding, _ := a.Configuration.Model["embedding"].(map[string]any)
			return embedding
		}
	}
	t.Fatalf("the feed does not release %s", g.agent())
	return nil
}

func TestConfigure_StoresAndReleasesTheEmbeddingAndRefusesItsChange(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)

	// A model other than the mock with no embedding is refused, and stores nothing.
	missing := newConfigureRequest(name(t, "request"), profile, "edited", 1)
	missing.Configuration.Model.Embedding = nil
	status, kind := configureKind(t, g, missing)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "embedding_required", kind)
	assert.Equal(t, 1, revisionCount(t, g.agent()))

	// Control: naming its embedding, the same change is stored and released with it.
	status, _ = configureKind(t, g, newConfigureRequest(name(t, "request"), profile, "edited", 1))
	require.Equal(t, http.StatusOK, status)
	sent, err := json.Marshal(newConfigureRequest("", profile, "", 1).Configuration.Model.Embedding)
	require.NoError(t, err)
	var want map[string]any
	require.NoError(t, json.Unmarshal(sent, &want))
	assert.Equal(t, want, releasedEmbedding(t, g), "the embedding is released as it was configured")

	// Changing the embedding's model once set is refused, and stores nothing.
	changed := newConfigureRequest(name(t, "request"), profile, "edited", 2)
	changed.Configuration.Model.Embedding.Name = "text-embedding-3-small"
	status, kind = configureKind(t, g, changed)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "embedding_immutable", kind)
	assert.Equal(t, 2, revisionCount(t, g.agent()))

	// Control: changing only its key is stored.
	rekeyed := newConfigureRequest(name(t, "request"), profile, "edited", 2)
	rekeyed.Configuration.Model.Embedding.APIKeyRef = "other-embeddings/key"
	status, _ = configureKind(t, g, rekeyed)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "other-embeddings/key", releasedEmbedding(t, g)["apiKeyRef"])
}

// releasedProfile is the profile the controller's feed releases for g's agent.
func releasedProfile(t *testing.T, g garam) map[string]any {
	t.Helper()
	resp, err := g.controllerClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var feed struct {
		Agents []struct {
			Agent   string         `json:"agent"`
			Profile map[string]any `json:"profile"`
		} `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	for _, a := range feed.Agents {
		if a.Agent == g.agent() {
			return a.Profile
		}
	}
	t.Fatalf("the feed does not release %s", g.agent())
	return nil
}

func TestDesired_CarriesTheProfilesWorkspaceSizeWhereItNamesOne(t *testing.T) {
	g := requireGaram(t)
	plain := seedRevision(t, g)
	sized := name(t, "profile")
	require.NoError(t, execute(t, `INSERT INTO profiles (organization, name, version, settings)
VALUES ($1, $2, 1, '{"storageSize": "1Gi", "workspaceStorageSize": "5Gi"}')`, g.org(), sized))

	status, _ := configureKind(t, g, newConfigureRequest(name(t, "request"), sized, "sized", 1))
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "5Gi", releasedProfile(t, g)["workspaceStorageSize"])

	// Control: a revision naming a profile with no workspace size leaves it off the wire.
	status, _ = configureKind(t, g, newConfigureRequest(name(t, "request"), plain, "plain", 2))
	require.Equal(t, http.StatusOK, status)
	assert.NotContains(t, releasedProfile(t, g), "workspaceStorageSize")
}
