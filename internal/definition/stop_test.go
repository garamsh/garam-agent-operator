package definition_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// stopInput is a console request to stop, or start, firstAgent under requestID.
func stopInput(requestID string) definition.StopInput {
	return definition.StopInput{Key: key(requestID), Binding: binding(requestID), Agent: firstAgent}
}

// activation is an activation request of firstAgent's revision 1 under requestID.
func activation(requestID string) definition.ActivationRequest {
	return definition.ActivationRequest{
		RequestID: requestID, Epoch: "1", Generation: strings.Repeat("a", 32), ConfigRevision: 1, PlacementPodUID: "pod",
	}
}

func TestStop_NothingIsActivatedWhileStoppedAndStartAdmitsIt(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	_, err := f.service.PrepareActivation(ctx, firstAgent, activation("a1"))
	require.NoError(t, err)
	require.NoError(t, f.service.RecordActivation(ctx, firstAgent, "a1", "activation-1"))

	stop, first, err := f.service.Stop(ctx, stopInput("stop-1"))
	require.NoError(t, err)
	require.True(t, first)
	assert.Equal(t, "activation-1", stop.ActivationID, "the stop does not name the activation to deactivate")
	assert.False(t, stop.Deactivated)

	_, err = f.service.PrepareActivation(ctx, firstAgent, activation("a2"))
	require.ErrorIs(t, err, definition.ErrAgentStopped)
	_, err = f.service.PrepareActivation(ctx, firstAgent, activation("a1"))
	require.ErrorIs(t, err, definition.ErrAgentStopped, "a repeat of an activation was admitted while stopped")

	require.NoError(t, f.service.RecordDeactivation(ctx, key("stop-1")))
	repeat, first, err := f.service.Stop(ctx, stopInput("stop-1"))
	require.NoError(t, err)
	assert.False(t, first)
	assert.True(t, repeat.Deactivated)

	started, err := f.service.Start(ctx, stopInput("start-1"))
	require.NoError(t, err)
	require.NotNil(t, started.Start)
	assert.Equal(t, key("start-1"), started.Start.Key)
	assert.Equal(t, key("stop-1"), started.Key, "the start ended another stop")

	// Control: once started, the next activation is admitted.
	_, err = f.service.PrepareActivation(ctx, firstAgent, activation("a2"))
	require.NoError(t, err)
}

func TestStop_OneHoldsTheAgentAndAStartNeedsOne(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	_, err := f.service.Start(ctx, stopInput("start-0"))
	require.ErrorIs(t, err, definition.ErrAgentNotStopped)

	stop, _, err := f.service.Stop(ctx, stopInput("stop-1"))
	require.NoError(t, err)
	assert.Empty(t, stop.ActivationID, "an agent never activated has an activation to deactivate")
	_, _, err = f.service.Stop(ctx, stopInput("stop-2"))
	require.ErrorIs(t, err, definition.ErrAgentStopped)

	changed := stopInput("stop-1")
	changed.Binding.BodySHA256 = other
	_, _, err = f.service.Stop(ctx, changed)
	require.ErrorIs(t, err, definition.ErrRequestReused)

	_, err = f.service.Start(ctx, stopInput("start-1"))
	require.NoError(t, err)
	// A repeat of the start answers it; another stop after it is a new one.
	repeat, err := f.service.Start(ctx, stopInput("start-1"))
	require.NoError(t, err)
	assert.Equal(t, key("start-1"), repeat.Start.Key)
	_, err = f.service.Start(ctx, stopInput("start-2"))
	require.ErrorIs(t, err, definition.ErrAgentNotStopped)
	_, first, err := f.service.Stop(ctx, stopInput("stop-2"))
	require.NoError(t, err)
	assert.True(t, first)
}

func TestStop_AnotherOrganizationsAgentIsNotFound(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	elsewhere := stopInput("stop-1")
	elsewhere.Key.Organization = globex

	_, _, err := f.service.Stop(ctx, elsewhere)
	require.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.Start(ctx, elsewhere)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the agent's own organization stops it.
	_, _, err = f.service.Stop(ctx, stopInput("stop-1"))
	require.NoError(t, err)
}

func TestStop_TheFeedCarriesStoppedAndThePositionMoves(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	page, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	require.Len(t, page.Revisions, 1)
	assert.False(t, page.Revisions[0].Stopped)

	_, _, err = f.service.Stop(ctx, stopInput("stop-1"))
	require.NoError(t, err)
	stopped, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	assert.Greater(t, stopped.Position, page.Position, "a stop did not wake the feed")
	assert.True(t, stopped.Revisions[0].Stopped)

	_, err = f.service.Start(ctx, stopInput("start-1"))
	require.NoError(t, err)
	started, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	assert.Greater(t, started.Position, stopped.Position)
	assert.False(t, started.Revisions[0].Stopped)
}
