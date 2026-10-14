package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func placementInput(pod, previous string) definition.PlacementInput {
	in := definition.PlacementInput{
		Agent: firstAgent, Controller: k8s,
		Request: definition.PlacementRequest{PodUID: pod, PVCUID: "pvc-1", Epoch: "1", TokenSHA256: "token of " + pod},
	}
	if previous != "" {
		in.Request.Previous = definition.PreviousPlacement{PodUID: previous, WriterStoppedSHA256: "evidence of " + previous}
	}
	return in
}

func TestRegisterPlacement_ARepeatAnswersThePlacementAsStored(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	first, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", ""))
	require.NoError(t, err)
	require.True(t, stored)

	repeat, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", ""))
	require.NoError(t, err)
	assert.False(t, stored, "a repeat was stored as a new placement")
	assert.Equal(t, first, repeat)
}

func TestRegisterPlacement_NothingRevivesASupersededPlacement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, _, err := f.service.RegisterPlacement(ctx, placementInput("pod-1", ""))
	require.NoError(t, err)
	_, _, err = f.service.RegisterPlacement(ctx, placementInput("pod-2", "pod-1"))
	require.NoError(t, err)

	_, _, err = f.service.RegisterPlacement(ctx, placementInput("pod-1", ""))
	require.ErrorIs(t, err, definition.ErrPlacementSuperseded)

	// Control: the placement that replaced it is held, and answered as a repeat.
	held, stored, err := f.service.RegisterPlacement(ctx, placementInput("pod-2", "pod-1"))
	require.NoError(t, err)
	assert.False(t, stored)
	assert.Equal(t, "pod-2", held.Request.PodUID)
}
