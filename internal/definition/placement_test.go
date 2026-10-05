package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func placementInput(pod, previous string, leaf string) definition.PlacementInput {
	in := definition.PlacementInput{
		Agent: firstAgent, Controller: k8s, LeafDER: []byte(leaf),
		Request: definition.PlacementRequest{PodUID: pod, PVCUID: "pvc-1", Epoch: "1", TokenSHA256: "token of " + pod},
	}
	if previous != "" {
		in.Request.Previous = definition.PreviousPlacement{PodUID: previous, WriterStoppedSHA256: "evidence of " + previous}
	}
	return in
}

func TestRegisterPlacement_RefreshReplacesTheLeafAndNothingElse(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", "", "leaf one"))
	require.NoError(t, err)
	require.True(t, stored)
	assert.Equal(t, []byte("leaf one"), first.LeafDER)

	refreshed, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", "", "leaf two"))
	require.NoError(t, err)
	assert.False(t, stored, "a refresh was stored as a new placement")
	assert.Equal(t, []byte("leaf two"), refreshed.LeafDER)
	assert.Equal(t, first.Request, refreshed.Request)

	// A repeat under the leaf now held answers it unchanged.
	repeat, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", "", "leaf two"))
	require.NoError(t, err)
	assert.False(t, stored)
	assert.Equal(t, refreshed, repeat)
}

func TestRegisterPlacement_NothingRevivesASupersededPlacement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, _, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", "", "leaf one"))
	require.NoError(t, err)
	_, _, err = f.service.RegisterPlacement(ctx, placementInput("pod-2", "pod-1", "leaf one"))
	require.NoError(t, err)

	for _, leaf := range []string{"leaf one", "leaf two"} {
		_, _, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", "", leaf))
		require.ErrorIs(t, err, definition.ErrPlacementSuperseded)
	}

	// Control: the placement that replaced it is held, and refreshed under another leaf.
	held, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-2", "pod-1", "leaf two"))
	require.NoError(t, err)
	assert.False(t, stored)
	assert.Equal(t, []byte("leaf two"), held.LeafDER)
}
