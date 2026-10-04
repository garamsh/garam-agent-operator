package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestRecordStatus_NeverLowersAField(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	_, err := f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)

	_, err = f.service.RecordStatus(ctx, firstAgent, 2, 2)
	require.NoError(t, err)
	late, err := f.service.RecordStatus(ctx, firstAgent, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, definition.Status{Observed: 2, Rendered: 2}, late)
}

func TestRecordStatus_RefusesARevisionTheAgentDoesNotHave(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	for _, r := range []definition.Revision{0, 2} {
		_, err := f.service.RecordStatus(ctx, firstAgent, r, 1)
		require.ErrorIs(t, err, definition.ErrInvalidStatus)
	}
	// Control: the agent's latest revision is one it has.
	_, err := f.service.RecordStatus(ctx, firstAgent, 1, 1)
	require.NoError(t, err)
}
