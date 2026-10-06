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

func TestPublishTemplate_RefusesAMalformedKeyReference(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	malformed := config("ego", nil)
	malformed.Model.APIKey = unseparatedKeyRef
	_, err := f.service.PublishTemplate(ctx, org, definition.Template{Name: brokenTemplate, Profile: f.profile, Config: malformed})
	assert.ErrorIs(t, err, definition.ErrInvalidSecretRef)
	_, err = f.repository.GetTemplate(ctx, org, definition.TemplateRef{Name: brokenTemplate, Version: 1})
	assert.ErrorIs(t, err, definition.ErrNotFound, "nothing is published")

	// Control: the same template with a well-formed reference, and one naming no model, publish.
	_, err = f.service.PublishTemplate(ctx, org, definition.Template{Name: brokenTemplate, Profile: f.profile, Config: config("ego", nil)})
	require.NoError(t, err)
	_, err = f.service.PublishTemplate(ctx, org, definition.Template{Name: "modelless", Profile: f.profile})
	require.NoError(t, err)
}

func TestPublishProfile_SettingsNoWorkloadCouldRunWithAreRefused(t *testing.T) {
	quantity := func(s string) *resource.Quantity { q := resource.MustParse(s); return &q }
	class := func(s string) *string { return &s }
	refused := map[string]func(*definition.ExecutionSettings){
		"no storage size":           func(s *definition.ExecutionSettings) { s.StorageSize = resource.Quantity{} },
		"a zero workspace size":     func(s *definition.ExecutionSettings) { s.WorkspaceStorageSize = quantity("0") },
		"a negative workspace size": func(s *definition.ExecutionSettings) { s.WorkspaceStorageSize = quantity("-1Gi") },
		"a storage class no name":   func(s *definition.ExecutionSettings) { s.StorageClassName = class("Fast_SSD") },
		"a request above its limit": func(s *definition.ExecutionSettings) {
			s.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")}
		},
	}
	for name, change := range refused {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			changed := settings("500m", "1Gi")
			change(&changed)
			_, err := f.service.PublishProfile(context.Background(), org, "checked", changed)
			assert.ErrorIs(t, err, definition.ErrInvalidProfile)
			_, _, err = f.service.PublishProfileVersion(context.Background(), org,
				definition.Profile{Name: "checked", Version: 1, Settings: changed})
			assert.ErrorIs(t, err, definition.ErrInvalidProfile)
			_, err = f.repository.GetProfile(context.Background(), org, definition.ProfileRef{Name: "checked", Version: 1})
			assert.ErrorIs(t, err, definition.ErrNotFound, "a refused profile was stored")
		})
	}

	// Control: the same settings with each named field well formed are published.
	f := newFixture(t)
	runnable := settings("500m", "1Gi")
	runnable.WorkspaceStorageSize = quantity("5Gi")
	runnable.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}
	_, created, err := f.service.PublishProfileVersion(context.Background(), org,
		definition.Profile{Name: "checked", Version: 1, Settings: runnable})
	require.NoError(t, err)
	assert.True(t, created)
}

func TestPublishProfileVersion_APublishedVersionIsNeverChanged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first := definition.Profile{Name: "pinned", Version: 1, Settings: settings("1", "1Gi")}
	published, created, err := f.service.PublishProfileVersion(ctx, org, first)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, definition.Version(1), published.Version)

	// The same settings again, written in another form, publish nothing.
	again := definition.Profile{Name: "pinned", Version: 1, Settings: settings("1000m", "1024Mi")}
	stored, created, err := f.service.PublishProfileVersion(ctx, org, again)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "1Gi", stored.Settings.StorageSize.String())

	// Other settings under the published version are refused, and the version keeps its own.
	changed := definition.Profile{Name: "pinned", Version: 1, Settings: settings("2", "1Gi")}
	_, _, err = f.service.PublishProfileVersion(ctx, org, changed)
	require.ErrorIs(t, err, definition.ErrProfileVersionConflict)
	kept, err := f.repository.GetProfile(ctx, org, definition.ProfileRef{Name: "pinned", Version: 1})
	require.NoError(t, err)
	assert.Equal(t, "1", kept.Settings.Resources.Requests.Cpu().String())

	// A version past the next is refused; the next one is published with its own settings.
	_, _, err = f.service.PublishProfileVersion(ctx, org, definition.Profile{Name: "pinned", Version: 3, Settings: changed.Settings})
	require.ErrorIs(t, err, definition.ErrProfileVersionGap)
	next, created, err := f.service.PublishProfileVersion(ctx, org, definition.Profile{Name: "pinned", Version: 2, Settings: changed.Settings})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "2", next.Settings.Resources.Requests.Cpu().String())
}
