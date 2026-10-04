package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestUpdateDefinition_StaleRevisionRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	_, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)

	// Control: an update based on the latest revision is accepted as the next revision.
	accepted, err := f.service.UpdateDefinition(ctx, definition.UpdateInput{
		Agent: firstAgent, BasedOn: 1, Profile: f.profile,
		Config: config("edited by one", definition.ToolPins{webFetch: firstPin}),
	})
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(2), accepted.Revision)

	_, err = f.service.UpdateDefinition(ctx, definition.UpdateInput{
		Agent: firstAgent, BasedOn: 1, Profile: f.profile,
		Config: config("edited by two", definition.ToolPins{"shell": "sha256:cc"}),
	})
	require.ErrorIs(t, err, definition.ErrStaleRevision)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(2), d.Revision)
	assert.Equal(t, "edited by one", d.Config.Ego)
	assert.Equal(t, definition.ToolPins{webFetch: firstPin}, d.Config.Tools)
}

func TestUpdateDefinition_UnregisteredAgentRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})

	update := definition.UpdateInput{
		Agent: firstAgent, BasedOn: 0, Profile: f.profile,
		Config: config("ego", definition.ToolPins{webFetch: firstPin}),
	}
	_, err := f.service.UpdateDefinition(ctx, update)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: once the agent is registered, an update based on its revision is accepted.
	_, err = f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	update.BasedOn = 1
	_, err = f.service.UpdateDefinition(ctx, update)
	require.NoError(t, err)
}

func TestUpdateDefinition_UnpublishedProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	_, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)

	update := definition.UpdateInput{
		Agent: firstAgent, BasedOn: 1, Profile: definition.ProfileRef{Name: "unpublished", Version: 1},
		Config: config("ego", definition.ToolPins{webFetch: firstPin}),
	}
	_, err = f.service.UpdateDefinition(ctx, update)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the same update naming a published profile version is accepted.
	update.Profile = f.profile
	_, err = f.service.UpdateDefinition(ctx, update)
	require.NoError(t, err)
}
