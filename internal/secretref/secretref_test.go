package secretref_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/secretref"
)

// unseparated is a reference with no "/", which names a Secret and no key.
const unseparated = "model-api-key"

func TestParse_AcceptsOnlyASecretNameAndKeyKubernetesAllows(t *testing.T) {
	for _, ref := range []string{
		"", unseparated, "/api-key", "model-api-key/", "model-api-key/api/key",
		"Model-Api-Key/api-key", "-model/api-key", "model-/api-key", "model_api/key",
		"model-api-key/api key", "model-api-key/.", "model-api-key/..", "model-api-key/café",
	} {
		_, _, err := secretref.Parse(ref)
		assert.ErrorIs(t, err, secretref.ErrInvalid, "%q", ref)
	}
	// Control: a DNS-subdomain name and a key of [-._a-zA-Z0-9]+ split into their parts.
	for ref, want := range map[string][2]string{
		"model-api-key/api-key":         {"model-api-key", "api-key"},
		"minimax.v2/API_KEY.1":          {"minimax.v2", "API_KEY.1"},
		"a/b":                           {"a", "b"},
		"model-api-key/.dot-leading_ok": {"model-api-key", ".dot-leading_ok"},
	} {
		name, key, err := secretref.Parse(ref)
		require.NoError(t, err, "%q", ref)
		assert.Equal(t, want, [2]string{name, key}, "%q", ref)
	}
}
