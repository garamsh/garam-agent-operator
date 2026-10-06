package distribution_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// rawAgents is the feed's answer for the controller as JSON objects, by agent.
func (e *env) rawAgents(t *testing.T) map[string]map[string]any {
	t.Helper()
	resp, err := e.withCert.Get(e.server.URL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 200, resp.StatusCode)
	var out struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	agents := map[string]map[string]any{}
	for _, a := range out.Agents {
		agents[a["agent"].(string)] = a
	}
	return agents
}

func TestDesired_CarriesTheEmbeddingAndTheWorkspaceSizeWhereARevisionNamesThem(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	workspace := resource.MustParse("5Gi")
	p, err := e.definitions.PublishProfile(ctx, orgID, "with-workspace", definition.ExecutionSettings{
		StorageSize: resource.MustParse("1Gi"), WorkspaceStorageSize: &workspace,
	})
	require.NoError(t, err)
	_, err = e.definitions.Configure(ctx, definition.ConfigureInput{
		Request: definition.RequestKey{Organization: orgID, RequestID: "with-embedding"},
		Binding: definition.Binding{
			Actor: "actor", Operation: "agent:configure", Target: agentA,
			Assignment: definition.Assignment{Operator: controller, Epoch: epoch},
		},
		Agent: agentA, ExpectedRevision: 2,
		Profile: definition.ProfileRef{Name: p.Name, Version: p.Version},
		Config: definition.Configuration{Model: definition.Model{
			Provider: "openai-compatible", BaseURL: "https://api.minimax.io/v1", Name: "MiniMax-M2", APIKey: "minimax/api-key",
			Embedding: &definition.Embedding{
				BaseURL: "https://embeddings.example/v1", Name: "bge-base-en-v1.5", APIKey: "embeddings/key",
			},
		}},
	})
	require.NoError(t, err)

	agents := e.rawAgents(t)
	require.Contains(t, agents, agentA)
	assert.Equal(t, map[string]any{
		"baseUrl": "https://embeddings.example/v1", "name": "bge-base-en-v1.5", "apiKeyRef": "embeddings/key",
	}, agents[agentA]["configuration"].(map[string]any)["model"].(map[string]any)["embedding"])
	assert.Equal(t, "5Gi", agents[agentA]["profile"].(map[string]any)["workspaceStorageSize"])

	// Control: agentB's revision names neither, and the wire leaves both out rather than empty.
	require.Contains(t, agents, agentB)
	assert.NotContains(t, agents[agentB]["configuration"].(map[string]any)["model"], "embedding")
	assert.NotContains(t, agents[agentB]["profile"], "workspaceStorageSize")
}
