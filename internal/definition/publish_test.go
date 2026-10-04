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
	first, err := f.service.PublishProfile(ctx, "large", input)
	require.NoError(t, err)

	input.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("64")
	*input.StorageClassName = "changed"
	first.Settings.StorageSize = resource.MustParse("1Ti")
	second, err := f.service.PublishProfile(ctx, "large", settings("4", "20Gi"))
	require.NoError(t, err)

	stored, err := f.repository.GetProfile(ctx, definition.ProfileRef{Name: "large", Version: first.Version})
	require.NoError(t, err)
	assert.Equal(t, definition.Version(1), stored.Version)
	assert.Equal(t, "2", stored.Settings.Resources.Requests.Cpu().String())
	assert.Equal(t, "10Gi", stored.Settings.StorageSize.String())
	assert.Equal(t, "standard", *stored.Settings.StorageClassName)

	// Control: the name's next publication is a new version carrying the new settings.
	latest, err := f.repository.GetProfile(ctx, definition.ProfileRef{Name: "large", Version: second.Version})
	require.NoError(t, err)
	assert.Equal(t, definition.Version(2), latest.Version)
	assert.Equal(t, "4", latest.Settings.Resources.Requests.Cpu().String())
}

func TestPublishTemplate_UnknownProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	_, err := f.service.PublishTemplate(ctx, definition.Template{
		Name:    "writer",
		Profile: definition.ProfileRef{Name: f.profile.Name, Version: f.profile.Version + 1},
		Config:  config("ego", definition.ToolPins{"web_fetch": firstPin}),
	})
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the same template naming a published profile version is accepted.
	_, err = f.service.PublishTemplate(ctx, definition.Template{
		Name:    "writer",
		Profile: f.profile,
		Config:  config("ego", definition.ToolPins{"web_fetch": firstPin}),
	})
	require.NoError(t, err)
}
