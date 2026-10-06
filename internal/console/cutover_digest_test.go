package console

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSourceDigest_IsGaramsDigestOfTheRFC8785JSON pins control's digest to the canonical text garam
// pins its own to (garam@1a5273d, internal/agent/cutover_test.go
// TestSourceDigest_IsTheSHA256OfTheRFC8785JSON): members ordered by UTF-16 code units, and only the
// quote, the backslash and the control characters escaped. It is white-box because the digest is
// unexported, and the vector is what both sides must agree on.
func TestSourceDigest_IsGaramsDigestOfTheRFC8785JSON(t *testing.T) {
	const (
		agent    = "grn:acme:default:agent:a1"
		operator = "grn:acme:default:operator:o1"
	)
	values := map[string]string{
		"b":          "2",
		"a":          "q\"<&> é\x01\\\n",
		"￿":          "last by UTF-16",
		"\U0001F600": "before U+FFFF by UTF-16, after it by bytes",
	}
	const canonical = `{"agentGrn":"grn:acme:default:agent:a1","operatorGrn":"grn:acme:default:operator:o1",` +
		`"values":{"a":"q\"<&>` + " " + `é\u0001\\\n","b":"2",` +
		`"` + "\U0001F600" + `":"before U+FFFF by UTF-16, after it by bytes","` + "￿" + `":"last by UTF-16"}}`
	want := sha256.Sum256([]byte(canonical))

	got, err := sourceDigest(agent, operator, values)
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(want[:]), got)

	// Control: encoding/json's own text of the same object is not the canonical one.
	marshalled, err := json.Marshal(map[string]any{"agentGrn": agent, "operatorGrn": operator, "values": values})
	require.NoError(t, err)
	sum := sha256.Sum256(marshalled)
	assert.NotEqual(t, hex.EncodeToString(sum[:]), got)
}
