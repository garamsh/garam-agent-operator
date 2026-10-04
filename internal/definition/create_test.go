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

	tools := definition.ToolPins{"web_fetch": firstPin}
	v1, err := f.service.PublishTemplate(ctx, definition.Template{Name: analyst, Profile: f.profile, Config: config("first ego", tools)})
	require.NoError(t, err)
	_, err = f.service.CreateAgent(ctx, key("r1"), definition.TemplateRef{Name: analyst, Version: v1.Version})
	require.NoError(t, err)

	tools["web_fetch"] = secondPin
	v2, err := f.service.PublishTemplate(ctx, definition.Template{Name: analyst, Profile: f.profile, Config: config("second ego", tools)})
	require.NoError(t, err)

	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
	assert.Equal(t, "first ego", d.Config.Ego)
	assert.Equal(t, definition.ToolPins{"web_fetch": firstPin}, d.Config.Tools)

	// Control: an agent created from the later version carries the later content.
	_, err = f.service.CreateAgent(ctx, key("r2"), definition.TemplateRef{Name: analyst, Version: v2.Version})
	require.NoError(t, err)
	later, err := f.service.GetDefinition(ctx, secondAgent)
	require.NoError(t, err)
	assert.Equal(t, "second ego", later.Config.Ego)
	assert.Equal(t, definition.ToolPins{"web_fetch": secondPin}, later.Config.Tools)
}

func TestCreateAgent_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	first, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, first.Outcome)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
	_, err = f.service.GetDefinition(ctx, secondAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: another request id is another creation, and registers a second agent.
	other, err := f.service.CreateAgent(ctx, key("r2"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: secondAgent}, other.Outcome)
}

func TestCreateAgent_RepeatedRequestReturnsFirstRefusal(t *testing.T) {
	ctx := context.Background()
	refusal := fmt.Errorf("organization over its agent quota: %w", definition.ErrRegistrationRefused)
	f := newFixture(t, registration{err: refusal}, registration{agent: firstAgent})

	first, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Failed{Reason: refusal.Error()}, first.Outcome)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Failed{Reason: refusal.Error()}, repeat.Outcome)
	_, err = f.service.GetDefinition(ctx, firstAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)
}

func TestCreateAgent_UnansweredRegistrationResumesOnRepeat(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{err: errors.New("connection reset")}, registration{agent: firstAgent})

	_, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.Error(t, err)
	require.NotErrorIs(t, err, definition.ErrRegistrationRefused)

	repeat, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
	d, err := f.service.GetDefinition(ctx, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
}

func TestCreateAgent_RequestIDReusedForAnotherTemplateRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent}, registration{agent: secondAgent})

	v2, err := f.service.PublishTemplate(ctx, definition.Template{Name: f.template.Name, Profile: f.profile, Config: config("second ego", definition.ToolPins{"web_fetch": secondPin})})
	require.NoError(t, err)
	_, err = f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)

	_, err = f.service.CreateAgent(ctx, key("r1"), definition.TemplateRef{Name: v2.Name, Version: v2.Version})
	require.ErrorIs(t, err, definition.ErrRequestReused)

	// Control: the same key naming the same template is a repeat, not a reuse.
	repeat, err := f.service.CreateAgent(ctx, key("r1"), f.template)
	require.NoError(t, err)
	assert.Equal(t, definition.Registered{Agent: firstAgent}, repeat.Outcome)
}
