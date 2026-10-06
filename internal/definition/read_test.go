package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// otherOrg is an organization beside org, drafter the template the publication tests publish, and
// otherName a name no test publishes first.
const (
	otherOrg  = "globex"
	drafter   = "drafter"
	otherName = "elsewhere"
)

// publishBinding is what an authority for a publication binds, for body.
func publishBinding(body string) definition.Binding {
	return definition.Binding{Actor: actor, Operation: "agent-template:publish", Target: "grn:root:default:org:" + org,
		BodySHA256: body, OperationRef: "ref-" + body}
}

func TestPublish_RecordsOneVersionPerRequest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	in := definition.PublishInput{
		Request: key("p1"), Binding: publishBinding("body-1"),
		Template: definition.Template{Name: drafter, Profile: f.profile, Config: config("first", nil)},
	}

	first, created, err := f.service.Publish(ctx, in)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, definition.TemplateRef{Name: drafter, Version: 1}, first.Template)

	// The same request again answers the version it published and publishes none.
	repeat, created, err := f.service.Publish(ctx, in)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, repeat)

	// Control: another request publishes the next version.
	next := in
	next.Request, next.Binding = key("p2"), publishBinding("body-2")
	second, created, err := f.service.Publish(ctx, next)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, definition.Version(2), second.Template.Version)

	// The first key under another body, or naming another template, is reused, and publishes nothing.
	reused := in
	reused.Binding = publishBinding("body-3")
	_, _, err = f.service.Publish(ctx, reused)
	assert.ErrorIs(t, err, definition.ErrRequestReused)
	renamed := in
	renamed.Template.Name = otherName
	_, _, err = f.service.Publish(ctx, renamed)
	assert.ErrorIs(t, err, definition.ErrRequestReused)
	_, err = f.service.GetTemplate(ctx, org, definition.TemplateRef{Name: drafter, Version: 3})
	assert.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.GetTemplate(ctx, org, definition.TemplateRef{Name: otherName, Version: 1})
	assert.ErrorIs(t, err, definition.ErrNotFound)
}

func TestPublish_ResolvesTheProfileInTheRequestsOrganizationOnly(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	foreign, err := f.service.PublishProfile(ctx, otherOrg, "foreign", settings("1", "1Gi"))
	require.NoError(t, err)

	// Control: the organization's own profile.
	_, _, err = f.service.Publish(ctx, definition.PublishInput{Request: key("own"), Binding: publishBinding("own"),
		Template: definition.Template{Name: drafter, Profile: f.profile}})
	require.NoError(t, err)

	_, _, err = f.service.Publish(ctx, definition.PublishInput{Request: key("foreign"), Binding: publishBinding("foreign"),
		Template: definition.Template{Name: drafter,
			Profile: definition.ProfileRef{Name: foreign.Name, Version: foreign.Version}}})
	assert.ErrorIs(t, err, definition.ErrNotFound)
}

func TestListTemplates_AnswersTheLatestOfEachOfTheOrganizationsOwn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: f.template.Name, Profile: f.profile,
		Config: config("second", nil)})
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, org, definition.Template{Name: "analyst", Profile: f.profile})
	require.NoError(t, err)
	foreign, err := f.service.PublishProfile(ctx, otherOrg, "small", settings("1", "1Gi"))
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, otherOrg, definition.Template{Name: "zebra",
		Profile: definition.ProfileRef{Name: foreign.Name, Version: foreign.Version}})
	require.NoError(t, err)

	latest, err := f.service.ListTemplates(ctx, org)
	require.NoError(t, err)
	require.Len(t, latest, 2)
	assert.Equal(t, definition.TemplateRef{Name: "analyst", Version: 1},
		definition.TemplateRef{Name: latest[0].Name, Version: latest[0].Version})
	assert.Equal(t, definition.TemplateRef{Name: f.template.Name, Version: 2},
		definition.TemplateRef{Name: latest[1].Name, Version: latest[1].Version})
	assert.Equal(t, "second", latest[1].Config.Ego)
}

func TestListProfiles_AnswersEveryVersionOfTheOrganizationsOwn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, err := f.service.PublishProfile(ctx, org, "small", settings("1", "2Gi"))
	require.NoError(t, err)
	_, err = f.service.PublishProfile(ctx, otherOrg, "huge", settings("8", "1Ti"))
	require.NoError(t, err)

	refs, err := f.service.ListProfiles(ctx, org)
	require.NoError(t, err)
	assert.Equal(t, []definition.ProfileRef{{Name: "small", Version: 1}, {Name: "small", Version: 2}}, refs)
}

func TestExecution_AnswersOnlyInTheAgentsOrganization(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: "grn:acme:default:agent:1"})
	_, _, err := f.service.CreateAgent(ctx, f.create("create", f.template))
	require.NoError(t, err)

	// Control: the agent's own organization reads it, with nothing rendered or reported yet.
	e, err := f.service.Execution(ctx, org, "grn:acme:default:agent:1")
	require.NoError(t, err)
	assert.Equal(t, definition.Execution{Desired: 1}, e)

	_, err = f.service.Execution(ctx, otherOrg, "grn:acme:default:agent:1")
	assert.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.Execution(ctx, org, "grn:acme:default:agent:nobody")
	assert.ErrorIs(t, err, definition.ErrNotFound)

	_, err = f.service.RecordStatus(ctx, "grn:acme:default:agent:1", 1, 1)
	require.NoError(t, err)
	e, err = f.service.Execution(ctx, org, "grn:acme:default:agent:1")
	require.NoError(t, err)
	assert.Equal(t, definition.Execution{Desired: 1, Rendered: 1}, e)
}
