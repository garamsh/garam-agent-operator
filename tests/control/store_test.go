//go:build e2e

package control_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

// concurrency is how many callers race for one revision or one creation key.
const concurrency = 8

// registrar answers each key with one GRN, as garam answers a repeated registration;
// a request id beginning "refuse" is refused.
type registrar struct{}

func (registrar) Register(_ context.Context, key definition.CreationKey) (definition.GRN, error) {
	if strings.HasPrefix(key.RequestID, "refuse") {
		return "", fmt.Errorf("over quota: %w", definition.ErrRegistrationRefused)
	}
	return definition.GRN("grn:acme:default:agent:" + key.RequestID), nil
}

// newService is a service over the PostgreSQL store, with a profile and template published
// under names unique to the test.
func newService(t *testing.T) (definition.Service, definition.ProfileRef, definition.TemplateRef) {
	t.Helper()
	ctx := context.Background()
	svc := definition.NewService(repository.NewPostgres(pool), registrar{})

	class := "standard"
	p, err := svc.PublishProfile(ctx, t.Name(), definition.ExecutionSettings{
		Resources:        corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		StorageSize:      resource.MustParse("1Gi"),
		StorageClassName: &class,
	})
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}

	tmpl, err := svc.PublishTemplate(ctx, definition.Template{Name: t.Name(), Profile: profile, Config: config("first ego")})
	require.NoError(t, err)
	return svc, profile, definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version}
}

func config(ego string) definition.Configuration {
	return definition.Configuration{
		Model: definition.Model{Provider: "anthropic", BaseURL: "https://api.anthropic.com", Name: "claude-opus-5-5", APIKey: "model-api-key"},
		Ego:   ego,
		Tools: definition.ToolPins{"web_fetch": "sha256:aa"},
	}
}

func key(t *testing.T, requestID string) definition.CreationKey {
	return definition.CreationKey{Actor: "grn:acme:default:user:7c1d", Organization: t.Name(), RequestID: requestID}
}

func TestBinary_ServesHealthOnTheSchemaItApplied(t *testing.T) {
	resp, err := http.Get(healthURL + "/healthz")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	for _, table := range []string{"profiles", "templates", "definitions", "creations"} {
		var exists bool
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		assert.True(t, exists, "table %s", table)
	}
}

func TestPostgres_StaleRevisionRefused(t *testing.T) {
	ctx := context.Background()
	svc, profile, tmpl := newService(t)
	created, err := svc.CreateAgent(ctx, key(t, "stale"), tmpl)
	require.NoError(t, err)
	agent := created.Outcome.(definition.Registered).Agent

	// Control: an update based on the latest revision is accepted as the next revision.
	accepted, err := svc.UpdateDefinition(ctx, definition.UpdateInput{Agent: agent, BasedOn: 1, Profile: profile, Config: config("edited by one")})
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(2), accepted.Revision)

	_, err = svc.UpdateDefinition(ctx, definition.UpdateInput{Agent: agent, BasedOn: 1, Profile: profile, Config: config("edited by two")})
	require.ErrorIs(t, err, definition.ErrStaleRevision)

	d, err := svc.GetDefinition(ctx, agent)
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(2), d.Revision)
	assert.Equal(t, "edited by one", d.Config.Ego)
}

func TestPostgres_ConcurrentUpdatesOnOneRevisionStoreOne(t *testing.T) {
	ctx := context.Background()
	svc, profile, tmpl := newService(t)
	created, err := svc.CreateAgent(ctx, key(t, "race"), tmpl)
	require.NoError(t, err)
	agent := created.Outcome.(definition.Registered).Agent

	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Go(func() {
			_, errs[i] = svc.UpdateDefinition(ctx, definition.UpdateInput{
				Agent: agent, BasedOn: 1, Profile: profile, Config: config(fmt.Sprintf("edit %d", i)),
			})
		})
	}
	wg.Wait()

	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
			continue
		}
		assert.ErrorIs(t, err, definition.ErrStaleRevision)
	}
	assert.Equal(t, 1, accepted)
	var revisions int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM definitions WHERE agent = $1", string(agent)).Scan(&revisions))
	assert.Equal(t, 2, revisions)
}

func TestPostgres_RepeatedCreationReturnsFirstOutcome(t *testing.T) {
	ctx := context.Background()
	svc, _, tmpl := newService(t)

	first, err := svc.CreateAgent(ctx, key(t, "repeat"), tmpl)
	require.NoError(t, err)
	repeat, err := svc.CreateAgent(ctx, key(t, "repeat"), tmpl)
	require.NoError(t, err)
	assert.Equal(t, first.Outcome, repeat.Outcome)

	refused, err := svc.CreateAgent(ctx, key(t, "refuse-1"), tmpl)
	require.NoError(t, err)
	repeatRefused, err := svc.CreateAgent(ctx, key(t, "refuse-1"), tmpl)
	require.NoError(t, err)
	assert.IsType(t, definition.Failed{}, refused.Outcome)
	assert.Equal(t, refused.Outcome, repeatRefused.Outcome)

	// Control: another request id is another creation.
	other, err := svc.CreateAgent(ctx, key(t, "other"), tmpl)
	require.NoError(t, err)
	assert.NotEqual(t, first.Outcome, other.Outcome)

	assert.Equal(t, 3, creations(t))
}

func TestPostgres_ConcurrentRepeatedCreationStoresOne(t *testing.T) {
	ctx := context.Background()
	svc, _, tmpl := newService(t)

	outcomes := make([]definition.Outcome, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Go(func() {
			var c definition.Creation
			c, errs[i] = svc.CreateAgent(ctx, key(t, "together"), tmpl)
			outcomes[i] = c.Outcome
		})
	}
	wg.Wait()

	for i := range concurrency {
		require.NoError(t, errs[i])
		assert.Equal(t, definition.Registered{Agent: "grn:acme:default:agent:together"}, outcomes[i])
	}
	assert.Equal(t, 1, creations(t))
	var revisions int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM definitions WHERE agent = $1", "grn:acme:default:agent:together").Scan(&revisions))
	assert.Equal(t, 1, revisions)
}

func TestPostgres_StoredValuesReadBackUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, profile, tmpl := newService(t)
	repo := repository.NewPostgres(pool)

	p, err := repo.GetProfile(ctx, profile)
	require.NoError(t, err)
	assert.Equal(t, "500m", p.Settings.Resources.Requests.Cpu().String())
	assert.Equal(t, "1Gi", p.Settings.StorageSize.String())
	require.NotNil(t, p.Settings.StorageClassName)
	assert.Equal(t, "standard", *p.Settings.StorageClassName)

	created, err := svc.CreateAgent(ctx, key(t, "values"), tmpl)
	require.NoError(t, err)
	_, err = svc.PublishTemplate(ctx, definition.Template{Name: tmpl.Name, Profile: profile, Config: config("second ego")})
	require.NoError(t, err)

	d, err := svc.GetDefinition(ctx, created.Outcome.(definition.Registered).Agent)
	require.NoError(t, err)
	assert.Equal(t, definition.Definition{Agent: d.Agent, Revision: 1, Profile: profile, Config: config("first ego")}, d)
}

func TestPostgres_UnknownRowsAreNotFound(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewPostgres(pool)

	_, err := repo.GetDefinition(ctx, "grn:acme:default:agent:never")
	assert.ErrorIs(t, err, definition.ErrNotFound, "definition")
	_, err = repo.GetProfile(ctx, definition.ProfileRef{Name: t.Name(), Version: 1})
	assert.ErrorIs(t, err, definition.ErrNotFound, "profile")
	err = repo.AppendDefinition(ctx, definition.Definition{Agent: "grn:acme:default:agent:never", Revision: 1})
	assert.ErrorIs(t, err, definition.ErrNotFound, "append")
}

// creations counts the creations stored under the test's organization.
func creations(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM creations WHERE organization = $1", t.Name()).Scan(&n))
	return n
}
