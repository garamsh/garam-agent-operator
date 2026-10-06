package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestPublishProfile_PublishedVersionNeverChanges(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	input := settings("2", "10Gi")
	first, err := f.service.PublishProfile(ctx, org, "large", input)
	require.NoError(t, err)

	input.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("64")
	*input.StorageClassName = "changed"
	first.Settings.StorageSize = resource.MustParse("1Ti")
	second, err := f.service.PublishProfile(ctx, org, "large", settings("4", "20Gi"))
	require.NoError(t, err)

	stored, err := f.repository.GetProfile(ctx, org, definition.ProfileRef{Name: "large", Version: first.Version})
	require.NoError(t, err)
	assert.Equal(t, definition.Version(1), stored.Version)
	assert.Equal(t, "2", stored.Settings.Resources.Requests.Cpu().String())
	assert.Equal(t, "10Gi", stored.Settings.StorageSize.String())
	assert.Equal(t, "standard", *stored.Settings.StorageClassName)

	// Control: the name's next publication is a new version carrying the new settings.
	latest, err := f.repository.GetProfile(ctx, org, definition.ProfileRef{Name: "large", Version: second.Version})
	require.NoError(t, err)
	assert.Equal(t, definition.Version(2), latest.Version)
	assert.Equal(t, "4", latest.Settings.Resources.Requests.Cpu().String())
}

func TestPublishTemplate_UnknownProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	_, err := f.service.PublishTemplate(ctx, org, definition.Template{
		Name:    "writer",
		Profile: definition.ProfileRef{Name: f.profile.Name, Version: f.profile.Version + 1},
		Config:  config("ego", definition.ToolPins{webFetch: firstPin}),
	})
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the same template naming a published profile version is accepted.
	_, err = f.service.PublishTemplate(ctx, org, definition.Template{
		Name:    "writer",
		Profile: f.profile,
		Config:  config("ego", definition.ToolPins{webFetch: firstPin}),
	})
	require.NoError(t, err)
}

// globex is a second organization, publishing under the same names as org.
const globex = "globex"

func TestPublish_TwoOrganizationsPublishOneNameIndependently(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// globex publishes the profile and template names org published in the fixture.
	p, err := f.service.PublishProfile(ctx, globex, f.profile.Name, settings("8", "100Gi"))
	require.NoError(t, err)
	assert.Equal(t, f.profile, definition.ProfileRef{Name: p.Name, Version: p.Version})
	tmpl, err := f.service.PublishTemplate(ctx, globex, definition.Template{
		Name:    f.template.Name,
		Profile: f.profile,
		Config:  config("globex ego", definition.ToolPins{webFetch: secondPin}),
	})
	require.NoError(t, err)
	assert.Equal(t, f.template, definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version})

	// Each organization reads its own content under the one name and version.
	ours, err := f.repository.GetProfile(ctx, org, f.profile)
	require.NoError(t, err)
	assert.Equal(t, "500m", ours.Settings.Resources.Requests.Cpu().String())
	theirs, err := f.repository.GetProfile(ctx, globex, f.profile)
	require.NoError(t, err)
	assert.Equal(t, "8", theirs.Settings.Resources.Requests.Cpu().String())
	ourTemplate, err := f.repository.GetTemplate(ctx, org, f.template)
	require.NoError(t, err)
	assert.Equal(t, "first ego", ourTemplate.Config.Ego)
	theirTemplate, err := f.repository.GetTemplate(ctx, globex, f.template)
	require.NoError(t, err)
	assert.Equal(t, "globex ego", theirTemplate.Config.Ego)

	// Control: publishing the name again within one organization is that organization's next version.
	next, err := f.service.PublishProfile(ctx, org, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)
	assert.Equal(t, definition.Version(2), next.Version)
}

func TestPublishTemplate_AnotherOrganizationsProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	editor := definition.Template{
		Name:    "editor",
		Profile: f.profile,
		Config:  config("ego", definition.ToolPins{webFetch: firstPin}),
	}

	// f.profile is published in org only.
	_, err := f.service.PublishTemplate(ctx, globex, editor)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: once globex publishes a profile under that name and version, the same template is accepted.
	_, err = f.service.PublishProfile(ctx, globex, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, globex, editor)
	require.NoError(t, err)
}
