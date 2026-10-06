package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestDesired_OffersTheLatestRevisionRecordedForTheController(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	// Revision 1, from the creation, is recorded for the controller the agent was created on.
	created, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	require.Len(t, created.Revisions, 1)
	assert.Equal(t, definition.Revision(1), created.Revisions[0].Definition.Revision)
	assert.Equal(t, &definition.Assignment{Operator: k8s, Epoch: "1"}, created.Revisions[0].Definition.Assignment)

	_, err = f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)
	_, err = f.service.Configure(ctx, f.configure("c2", "second", 2))
	require.NoError(t, err)
	page, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	require.Len(t, page.Revisions, 1)
	assert.Equal(t, definition.Revision(3), page.Revisions[0].Definition.Revision)
	assert.Equal(t, &definition.Assignment{Operator: k8s, Epoch: "7"}, page.Revisions[0].Definition.Assignment)

	// The set is whole on every read, not only what was stored since the last.
	again, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	assert.Equal(t, page, again)

	// Control: another controller is offered nothing of it.
	other, err := f.service.Desired(ctx, "grn:acme:default:operator:other", 10)
	require.NoError(t, err)
	assert.Empty(t, other.Revisions)
}

func TestPosition_MovesWithEveryStoredRevision(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	before, err := f.service.Position(ctx)
	require.NoError(t, err)

	_, err = f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)
	after, err := f.service.Position(ctx)
	require.NoError(t, err)
	assert.Greater(t, after, before)

	// Control: a request that stores nothing leaves the position where it was.
	_, err = f.service.Configure(ctx, f.configure("c2", "stale", 1))
	require.ErrorIs(t, err, definition.ErrStaleRevision)
	unmoved, err := f.service.Position(ctx)
	require.NoError(t, err)
	assert.Equal(t, after, unmoved)
}
