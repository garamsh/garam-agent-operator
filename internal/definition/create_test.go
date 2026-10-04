package definition_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const (
	firstAgent  = definition.GRN("grn:acme:default:agent:0a1b2c3d4e5f6071")
	secondAgent = definition.GRN("grn:acme:default:agent:9f2ac1b40d8e7a35")

	analyst = "analyst"
)

func TestCreateAgent_StoresRevisionOneRecordedForItsAssignment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent, epoch: "3"})

	c, first, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
	assert.True(t, first)
	assert.Equal(t, definition.Registered{Agent: firstAgent, Epoch: "3"}, c.Outcome)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
	assert.Equal(t, f.profile, d.Profile)
	assert.Equal(t, "first ego", d.Config.Ego)
	assert.Equal(t, &definition.Assignment{Operator: k8s, Epoch: "3"}, d.Assignment)
}

func TestCreateAgent_LaterTemplateVersionLeavesAgentUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	tools := definition.ToolPins{webFetch: firstPin}
	v1, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: analyst, Profile: f.profile, Config: config("first ego", tools)})
	require.NoError(t, err)
	_, _, err = f.service.CreateAgent(ctx, f.create("r1", definition.TemplateRef{Name: analyst, Version: v1.Version}))
	require.NoError(t, err)

	tools[webFetch] = secondPin
	v2, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: analyst, Profile: f.profile, Config: config("second ego", tools)})
	require.NoError(t, err)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, "first ego", d.Config.Ego)
	assert.Equal(t, definition.ToolPins{webFetch: firstPin}, d.Config.Tools)

	// Control: an agent created from the later version carries the later content.
	_, _, err = f.service.CreateAgent(ctx, f.create("r2", definition.TemplateRef{Name: analyst, Version: v2.Version}))
	require.NoError(t, err)
	later, err := f.service.GetDefinition(ctx, secondAgent)
	require.NoError(t, err)
	assert.Equal(t, "second ego", later.Config.Ego)
}

func TestCreateAgent_RepeatedRequestAsksGaramAgainAndReturnsFirstOutcome(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: firstAgent})

	first, created, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
	require.True(t, created)

	repeat, created, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Outcome, repeat.Outcome)
	assert.Equal(t, 2, f.registrar.calls, "a repeat was answered without garam's recheck")
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
}

func TestCreateAgent_RepeatAfterTheAgentMovedRefused(t *testing.T) {
	tests := []struct {
		name   string
		repeat registration
	}{
		{"garam refuses the repeat as moved", registration{err: fmt.Errorf("moved: %w", definition.ErrRegistrationConflict)}},
		{"garam answers another epoch", registration{agent: firstAgent, epoch: "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, registration{agent: firstAgent}, registration{agent: firstAgent}, tt.repeat)
			_, _, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)

			// Control: a repeat garam answers with the same agent and epoch is the first outcome.
			_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)

			_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.ErrorIs(t, err, definition.ErrAssignmentMoved)
		})
	}
}

func TestCreateAgent_RefusalIsStoredAndAnsweredWithoutAskingAgain(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		conflict bool
	}{
		{"current authority refuses", fmt.Errorf("not authorized: %w", definition.ErrRegistrationRefused), false},
		{"another request holds the id", fmt.Errorf("conflict: %w", definition.ErrRegistrationConflict), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, registration{err: tt.err}, registration{agent: firstAgent})

			c, _, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)
			assert.Equal(t, definition.Failed{Reason: tt.err.Error(), Conflict: tt.conflict}, c.Outcome)

			repeat, _, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)
			assert.Equal(t, c.Outcome, repeat.Outcome)
			assert.Equal(t, 1, f.registrar.calls)
			_, err = f.service.GetDefinition(ctx, firstAgent)
			require.ErrorIs(t, err, definition.ErrNotFound)

			// Control: another request id is another creation, and garam is asked.
			other, _, err := f.service.CreateAgent(ctx, f.create("r2", f.template))
			require.NoError(t, err)
			assert.Equal(t, definition.Registered{Agent: firstAgent, Epoch: "1"}, other.Outcome)
		})
	}
}

func TestCreateAgent_UnansweredRegistrationResumesOnRepeat(t *testing.T) {
	ctx := context.Background()
	undecided := fmt.Errorf("no answer: %w", definition.ErrRegistrationUndecided)
	f := newFixture(t, registration{err: undecided}, registration{agent: firstAgent})

	_, _, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.ErrorIs(t, err, definition.ErrRegistrationUndecided)
	_, err = f.service.GetDefinition(ctx, firstAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	repeat, created, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, definition.Registered{Agent: firstAgent, Epoch: "1"}, repeat.Outcome)
}

func TestCreateAgent_RequestIDReusedForAnotherRequestRefused(t *testing.T) {
	tests := []struct {
		name   string
		change func(f fixture, in *definition.CreateInput)
	}{
		{"another actor", func(_ fixture, in *definition.CreateInput) { in.Binding.Actor = "grn:acme:default:user:other" }},
		{"another body", func(_ fixture, in *definition.CreateInput) { in.Binding.BodySHA256 = other }},
		{"another reference", func(_ fixture, in *definition.CreateInput) { in.Binding.OperationRef = other }},
		{"another controller", func(_ fixture, in *definition.CreateInput) {
			in.Controller, in.Binding.Target = "grn:acme:default:operator:other", "grn:acme:default:operator:other"
		}},
		{"another template", func(f fixture, in *definition.CreateInput) {
			in.Template = definition.TemplateRef{Name: f.template.Name, Version: f.template.Version + 1}
		}},
		{"another profile", func(f fixture, in *definition.CreateInput) {
			in.Profile = definition.ProfileRef{Name: f.profile.Name, Version: f.profile.Version + 1}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, registration{agent: firstAgent}, registration{agent: firstAgent})
			_, err := f.service.PublishProfile(ctx, org, f.profile.Name, settings("1", "2Gi"))
			require.NoError(t, err)
			_, err = f.service.PublishTemplate(ctx, org, definition.Template{Name: f.template.Name, Profile: f.profile})
			require.NoError(t, err)
			_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)

			reused := f.create("r1", f.template)
			tt.change(f, &reused)
			_, _, err = f.service.CreateAgent(ctx, reused)
			require.ErrorIs(t, err, definition.ErrRequestReused)

			// Control: the identical request under the key is a repeat, not a reuse.
			_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
			require.NoError(t, err)
		})
	}
}

func TestCreateAgent_UnpublishedProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})

	in := f.create("r1", f.template)
	in.Profile = definition.ProfileRef{Name: "unpublished", Version: 1}
	_, _, err := f.service.CreateAgent(ctx, in)
	require.ErrorIs(t, err, definition.ErrNotFound)
	assert.Equal(t, 0, f.registrar.calls)

	// Control: the same request naming a published profile version is created.
	_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
}

// inGlobex is f.create in globex, the organization that did not publish f.template or f.profile.
func (f fixture) inGlobex(requestID string) definition.CreateInput {
	in := f.create(requestID, f.template)
	in.Request.Organization = globex
	return in
}

func TestCreateAgent_AnotherOrganizationsTemplateRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	_, err := f.service.PublishProfile(ctx, globex, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)

	// f.template is published in org only; globex holds the profile the request names.
	_, _, err = f.service.CreateAgent(ctx, f.inGlobex("r1"))
	require.ErrorIs(t, err, definition.ErrNotFound)
	assert.Equal(t, 0, f.registrar.calls)

	// Control: once globex publishes a template under that name and version, the same request
	// creates the agent from globex's content.
	_, err = f.service.PublishTemplate(ctx, globex, definition.Template{
		Name:    f.template.Name,
		Profile: f.profile,
		Config:  config("globex ego", definition.ToolPins{webFetch: secondPin}),
	})
	require.NoError(t, err)
	_, _, err = f.service.CreateAgent(ctx, f.inGlobex("r1"))
	require.NoError(t, err)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, globex, d.Organization)
	assert.Equal(t, "globex ego", d.Config.Ego)
}

func TestCreateAgent_AnotherOrganizationsProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	// globex publishes the template the request names, under a profile of its own.
	small, err := f.service.PublishProfile(ctx, globex, "globex-small", settings("1", "2Gi"))
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, globex, definition.Template{
		Name:    f.template.Name,
		Profile: definition.ProfileRef{Name: small.Name, Version: small.Version},
	})
	require.NoError(t, err)

	// f.profile is published in org only.
	_, _, err = f.service.CreateAgent(ctx, f.inGlobex("r1"))
	require.ErrorIs(t, err, definition.ErrNotFound)
	assert.Equal(t, 0, f.registrar.calls)

	// Control: once globex publishes a profile under that name and version, the same request is created.
	_, err = f.service.PublishProfile(ctx, globex, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)
	_, _, err = f.service.CreateAgent(ctx, f.inGlobex("r1"))
	require.NoError(t, err)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, globex, d.Organization)
}
