package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// k8s is the controller binding() records every configure for.
const k8s = "grn:acme:default:operator:k8s"

func TestDesired_OffersOnlyLatestRevisionsRecordedForTheControllerAfterACursor(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	// Revision 1, from the creation, records no assignment and is offered to no controller.
	none, err := f.service.Desired(ctx, k8s, 0)
	require.NoError(t, err)
	assert.Empty(t, none.Revisions)

	_, err = f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)
	page, err := f.service.Desired(ctx, k8s, 0)
	require.NoError(t, err)
	require.Len(t, page.Revisions, 1)
	assert.Equal(t, definition.Revision(2), page.Revisions[0].Definition.Revision)
	assert.Equal(t, &definition.Assignment{Operator: k8s, Epoch: "7"}, page.Revisions[0].Definition.Assignment)

	other, err := f.service.Desired(ctx, "grn:acme:default:operator:other", 0)
	require.NoError(t, err)
	assert.Empty(t, other.Revisions)

	after, err := f.service.Desired(ctx, k8s, page.Position)
	require.NoError(t, err)
	assert.Empty(t, after.Revisions)
	assert.Equal(t, page.Position, after.Position)

	// Control: a revision stored after the cursor is offered, as the agent's latest.
	_, err = f.service.Configure(ctx, f.configure("c2", "second", 2))
	require.NoError(t, err)
	newer, err := f.service.Desired(ctx, k8s, page.Position)
	require.NoError(t, err)
	require.Len(t, newer.Revisions, 1)
	assert.Equal(t, definition.Revision(3), newer.Revisions[0].Definition.Revision)
	assert.Greater(t, newer.Position, page.Position)
}
