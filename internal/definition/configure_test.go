package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// binding is what an authority for a configure request to firstAgent would bind.
func binding(digest string) definition.Binding {
	return definition.Binding{
		Actor:        actor,
		Operation:    "agent:configure",
		Target:       string(firstAgent),
		BodySHA256:   digest,
		OperationRef: "ref-" + digest,
		Assignment:   definition.Assignment{Operator: "grn:acme:default:operator:k8s", Epoch: "7"},
	}
}

// configure is a configure request to firstAgent expecting revision expected.
func (f fixture) configure(requestID, ego string, expected definition.Revision) definition.ConfigureInput {
	return definition.ConfigureInput{
		Request:          key(requestID),
		Binding:          binding(requestID + "-" + ego),
		Agent:            firstAgent,
		ExpectedRevision: expected,
		Profile:          f.profile,
		Config:           config(ego, definition.ToolPins{webFetch: firstPin}),
	}
}

// registered is a fixture with firstAgent registered at revision 1.
func registered(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t, registration{agent: firstAgent})
	_, err := f.service.CreateAgent(context.Background(), key("create"), actor, f.template)
	require.NoError(t, err)
	return f
}

func TestConfigure_StaleRevisionRefused(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	// Control: a request expecting the latest revision is stored as the next revision.
	accepted, err := f.service.Configure(ctx, f.configure("c1", "edited by one", 1))
	require.NoError(t, err)
	assert.Equal(t, definition.Applied{Revision: 2}, accepted)

	_, err = f.service.Configure(ctx, f.configure("c2", "edited by two", 1))
	require.ErrorIs(t, err, definition.ErrStaleRevision)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(2), d.Revision)
	assert.Equal(t, "edited by one", d.Config.Ego)
}

func TestConfigure_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	first, err := f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)
	_, err = f.service.Configure(ctx, f.configure("c2", "second", 2))
	require.NoError(t, err)

	repeat, err := f.service.Configure(ctx, f.configure("c1", "first", 1))
	require.NoError(t, err)
	assert.Equal(t, first, repeat)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(3), d.Revision)
	assert.Equal(t, "second", d.Config.Ego)

	// Control: the same change under another request id is a new request, and stale.
	_, err = f.service.Configure(ctx, f.configure("c3", "first", 1))
	require.ErrorIs(t, err, definition.ErrStaleRevision)
	// A refused request's repeat returns its refusal, not a revision applied since.
	_, err = f.service.Configure(ctx, f.configure("c3", "first", 1))
	require.ErrorIs(t, err, definition.ErrStaleRevision)
}

func TestConfigure_RequestIDReusedForAnotherBindingRefused(t *testing.T) {
	tests := []struct {
		name   string
		change func(*definition.ConfigureInput)
	}{
		{"another actor", func(in *definition.ConfigureInput) { in.Binding.Actor = "grn:acme:default:user:other" }},
		{"another operation", func(in *definition.ConfigureInput) { in.Binding.Operation = "agent:create" }},
		{"another target", func(in *definition.ConfigureInput) { in.Binding.Target = string(secondAgent) }},
		{"another body", func(in *definition.ConfigureInput) { in.Binding.BodySHA256 = "other" }},
		{"another operation reference", func(in *definition.ConfigureInput) { in.Binding.OperationRef = "other" }},
		{"another assignment", func(in *definition.ConfigureInput) { in.Binding.Assignment.Epoch = "8" }},
		{"another agent", func(in *definition.ConfigureInput) { in.Agent = secondAgent }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := registered(t)
			_, err := f.service.Configure(ctx, f.configure("c1", "first", 1))
			require.NoError(t, err)

			reused := f.configure("c1", "first", 1)
			tt.change(&reused)
			_, err = f.service.Configure(ctx, reused)
			require.ErrorIs(t, err, definition.ErrRequestReused)

			// Control: the identical request under the key is a repeat, not a reuse.
			_, err = f.service.Configure(ctx, f.configure("c1", "first", 1))
			require.NoError(t, err)
		})
	}
}

func TestConfigure_UnregisteredAgentRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})

	_, err := f.service.Configure(ctx, f.configure("c1", "ego", 1))
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: once the agent is registered, the same request is accepted.
	_, err = f.service.CreateAgent(ctx, key("create"), actor, f.template)
	require.NoError(t, err)
	_, err = f.service.Configure(ctx, f.configure("c1", "ego", 1))
	require.NoError(t, err)
}

func TestConfigure_UnpublishedProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	in := f.configure("c1", "ego", 1)
	in.Profile = definition.ProfileRef{Name: "unpublished", Version: 1}
	_, err := f.service.Configure(ctx, in)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the same request naming a published profile version is accepted.
	_, err = f.service.Configure(ctx, f.configure("c1", "ego", 1))
	require.NoError(t, err)
}
