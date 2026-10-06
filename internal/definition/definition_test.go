package definition_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestParseRevision_ReadsOnlyACanonicalDecimalString(t *testing.T) {
	for _, s := range []string{"", "0", "03", "-1", "+1", "1.0", " 1", "1e3", "9223372036854775808"} {
		_, err := definition.ParseRevision(s)
		assert.ErrorIs(t, err, definition.ErrInvalidRevision, "%q", s)
	}
	// Control: canonical revisions read, and print back as they were written.
	for _, s := range []string{"1", "3", "10", "9223372036854775807"} {
		r, err := definition.ParseRevision(s)
		require.NoError(t, err, "%q", s)
		assert.Equal(t, s, r.String())
	}
}

// unseparatedKeyRef is a key reference with no "/", which names a Secret and no key.
const unseparatedKeyRef = "model-api-key"

// brokenTemplate names a template whose key reference is malformed.
const brokenTemplate = "broken"

func TestSecretRefParts_AcceptsOnlyASecretNameAndKeyKubernetesAllows(t *testing.T) {
	for _, ref := range []string{
		"", unseparatedKeyRef, "/api-key", "model-api-key/", "model-api-key/api/key",
		"Model-Api-Key/api-key", "-model/api-key", "model-/api-key", "model_api/key",
		"model-api-key/api key", "model-api-key/.", "model-api-key/..", "model-api-key/café",
	} {
		_, _, err := definition.SecretRef(ref).Parts()
		assert.ErrorIs(t, err, definition.ErrInvalidSecretRef, "%q", ref)
	}
	// Control: a DNS-subdomain name and a key of [-._a-zA-Z0-9]+ split into their parts.
	for ref, want := range map[string][2]string{
		"model-api-key/api-key":         {"model-api-key", "api-key"},
		"minimax.v2/API_KEY.1":          {"minimax.v2", "API_KEY.1"},
		"a/b":                           {"a", "b"},
		"model-api-key/.dot-leading_ok": {"model-api-key", ".dot-leading_ok"},
	} {
		name, key, err := definition.SecretRef(ref).Parts()
		require.NoError(t, err, "%q", ref)
		assert.Equal(t, want, [2]string{name, key}, "%q", ref)
	}
}
