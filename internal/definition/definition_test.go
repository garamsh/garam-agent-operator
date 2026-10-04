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
