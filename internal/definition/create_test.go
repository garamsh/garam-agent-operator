package definition_test

import (
	"context"
	"errors"
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

func TestCreateAgent_LaterTemplateVersionLeavesAgentUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	tools := definition.ToolPins{webFetch: firstPin}
	v1, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: analyst, Profile: f.profile, Config: config("first ego", tools)})
	require.NoError(t, err)
	_, err = f.service.CreateAgent(ctx, key("r1"), actor, definition.TemplateRef{Name: analyst, Version: v1.Version})
	require.NoError(t, err)

	tools[webFetch] = secondPin
	v2, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: analyst, Profile: f.profile, Config: config("second ego", tools)})
	require.NoError(t, err)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
	assert.Equal(t, "first ego", d.Config.Ego)
	assert.Equal(t, definition.ToolPins{webFetch: firstPin}, d.Config.Tools)

	// Control: an agent created from the later version carries the later content.
	_, err = f.service.CreateAgent(ctx, key("r2"), actor, definition.TemplateRef{Name: analyst, Version: v2.Version})
	require.NoError(t, err)
	later, err := f.service.GetDefinition(ctx, secondAgent)
	require.NoError(t, err)
	assert.Equal(t, "second ego", later.Config.Ego)
	assert.Equal(t, definition.ToolPins{webFetch: secondPin}, later.Config.Tools)
}

func TestCreateAgent_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	first, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, first.Outcome)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
	_, err = f.service.GetDefinition(ctx, secondAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: another request id is another creation, and registers a second agent.
	other, err := f.service.CreateAgent(ctx, key("r2"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: secondAgent}, other.Outcome)
}

func TestCreateAgent_RepeatedRequestReturnsFirstRefusal(t *testing.T) {
	ctx := context.Background()
	refusal := fmt.Errorf("organization over its agent quota: %w", definition.ErrRegistrationRefused)
	f := newFixture(t, registration{err: refusal}, registration{agent: firstAgent})

	first, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Failed{Reason: refusal.Error()}, first.Outcome)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Failed{Reason: refusal.Error()}, repeat.Outcome)
	_, err = f.service.GetDefinition(ctx, firstAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)
}

func TestCreateAgent_UnansweredRegistrationResumesOnRepeat(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{err: errors.New("connection reset")}, registration{agent: firstAgent})

	_, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.Error(t, err)
	require.NotErrorIs(t, err, definition.ErrRegistrationRefused)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
}

func TestCreateAgent_RequestIDReusedForAnotherTemplateRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	v2, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: f.template.Name, Profile: f.profile, Config: config("second ego", definition.ToolPins{webFetch: secondPin})})
	require.NoError(t, err)
	_, err = f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)

	_, err = f.service.CreateAgent(ctx, key("r1"), actor, definition.TemplateRef{Name: v2.Name, Version: v2.Version})
	require.ErrorIs(t, err, definition.ErrRequestReused)

	// Control: the same key naming the same template is a repeat, not a reuse.
	repeat, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
}

func TestCreateAgent_RequestIDReusedByAnotherActorRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})
	_, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)

	_, err = f.service.CreateAgent(ctx, key("r1"), "grn:acme:default:user:other", f.template)
	require.ErrorIs(t, err, definition.ErrRequestReused)

	// Control: the same actor repeating the key is a repeat, not a reuse.
	repeat, err := f.service.CreateAgent(ctx, key("r1"), actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
}

func TestCreateAgent_AnotherOrganizationsTemplateRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	inGlobex := definition.RequestKey{Organization: globex, RequestID: "r1"}

	// f.template is published in org only.
	_, err := f.service.CreateAgent(ctx, inGlobex, actor, f.template)
	require.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.GetDefinition(ctx, firstAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: once globex publishes a template under that name and version, the same request
	// creates the agent from globex's content.
	p, err := f.service.PublishProfile(ctx, globex, "small", settings("1", "2Gi"))
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, globex, definition.Template{
		Name:    f.template.Name,
		Profile: definition.ProfileRef{Name: p.Name, Version: p.Version},
		Config:  config("globex ego", definition.ToolPins{webFetch: secondPin}),
	})
	require.NoError(t, err)
	created, err := f.service.CreateAgent(ctx, inGlobex, actor, f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, created.Outcome)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, globex, d.Organization)
	assert.Equal(t, "globex ego", d.Config.Ego)
}
