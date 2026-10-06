package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const (
	// mockProvider is the model provider sherlock runs offline, with no embeddings endpoint.
	mockProvider = "mock"
	// otherEmbeddingName is an embedding model other than config's.
	otherEmbeddingName = "text-embedding-3-small"
)

// withModel is config's configuration with its model changed by change.
func withModel(change func(*definition.Model)) definition.Configuration {
	c := config("ego", definition.ToolPins{webFetch: firstPin})
	change(&c.Model)
	return c
}

// registeredOnTheMock is a fixture with firstAgent registered at revision 1 from a template whose
// model is the mock, naming no embedding.
func registeredOnTheMock(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	mock, err := f.service.PublishTemplate(ctx, org, definition.Template{
		Name: "offline", Profile: f.profile,
		Config: withModel(func(m *definition.Model) { m.Provider, m.Embedding = mockProvider, nil }),
	})
	require.NoError(t, err)
	_, _, err = f.service.CreateAgent(ctx, f.create("create", definition.TemplateRef{Name: mock.Name, Version: mock.Version}))
	require.NoError(t, err)
	return f
}

// embeddingRefusals are the models check refuses, each with the error it is refused with.
var embeddingRefusals = []struct {
	name  string
	model func(*definition.Model)
	err   error
}{
	{"a model other than the mock naming no embedding", func(m *definition.Model) { m.Embedding = nil },
		definition.ErrEmbeddingRequired},
	{"an embedding missing its base URL", func(m *definition.Model) { m.Embedding.BaseURL = "" },
		definition.ErrEmbeddingRequired},
	{"an embedding missing its name", func(m *definition.Model) { m.Embedding.Name = "" },
		definition.ErrEmbeddingRequired},
	{"an embedding with a malformed key reference", func(m *definition.Model) { m.Embedding.APIKey = unseparatedKeyRef },
		definition.ErrInvalidSecretRef},
}

// embeddingAccepted are the models check accepts beside each refusal.
var embeddingAccepted = []struct {
	name  string
	model func(*definition.Model)
}{
	{"a model with its embedding", func(*definition.Model) {}},
	{"an embedding taking no key", func(m *definition.Model) { m.Embedding.APIKey = "" }},
	{"the mock naming no embedding", func(m *definition.Model) { m.Provider, m.Embedding = mockProvider, nil }},
	{"the mock naming an embedding", func(m *definition.Model) { m.Provider = mockProvider }},
}

func TestConfigure_RefusesAModelItsAgentCouldNotStartWithAndStoresNothing(t *testing.T) {
	for _, tt := range embeddingRefusals {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := registered(t)
			in := f.configure("c1", "ego", 1)
			in.Config = withModel(tt.model)

			_, err := f.service.Configure(ctx, in)
			require.ErrorIs(t, err, tt.err)
			d, err := f.service.GetDefinition(ctx, firstAgent)
			require.NoError(t, err)
			assert.Equal(t, definition.Revision(1), d.Revision)

			// Control: the same request under the same id, naming its embedding, is stored.
			_, err = f.service.Configure(ctx, f.configure("c1", "ego", 1))
			require.NoError(t, err)
		})
	}
	for _, tt := range embeddingAccepted {
		t.Run("control: "+tt.name, func(t *testing.T) {
			f := registeredOnTheMock(t)
			in := f.configure("c1", "ego", 1)
			in.Config = withModel(tt.model)
			_, err := f.service.Configure(context.Background(), in)
			require.NoError(t, err)
			d, err := f.service.GetDefinition(context.Background(), firstAgent)
			require.NoError(t, err)
			assert.Equal(t, in.Config.Model, d.Config.Model, "the model is stored as configured")
		})
	}
}

func TestPublishTemplate_RefusesAModelItsAgentCouldNotStartWith(t *testing.T) {
	for _, tt := range embeddingRefusals {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			_, err := f.service.PublishTemplate(ctx, org, definition.Template{
				Name: brokenTemplate, Profile: f.profile, Config: withModel(tt.model),
			})
			require.ErrorIs(t, err, tt.err)
			_, err = f.repository.GetTemplate(ctx, org, definition.TemplateRef{Name: brokenTemplate, Version: 1})
			require.ErrorIs(t, err, definition.ErrNotFound, "nothing is published")

			// Control: the same template naming its embedding is published.
			_, err = f.service.PublishTemplate(ctx, org, definition.Template{
				Name: brokenTemplate, Profile: f.profile, Config: withModel(func(*definition.Model) {}),
			})
			require.NoError(t, err)
		})
	}
}

func TestCreateAgent_RefusesATemplateItsAgentCouldNotStartWith(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	// Stored straight into the repository: publishing refuses it now, so this stands for a
	// template published before the rule.
	missing, err := f.repository.PublishTemplate(ctx, org, definition.Template{
		Name: brokenTemplate, Profile: f.profile, Config: withModel(func(m *definition.Model) { m.Embedding = nil }),
	})
	require.NoError(t, err)

	_, _, err = f.service.CreateAgent(ctx, f.create("r1", definition.TemplateRef{Name: missing.Name, Version: missing.Version}))
	require.ErrorIs(t, err, definition.ErrEmbeddingRequired)
	assert.Equal(t, 0, f.registrar.calls, "garam was asked to create an agent its template could not start")

	// Control: the same request naming the fixture's template, which names its embedding, is created.
	_, _, err = f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)
}

func TestConfigure_RefusesChangingOrRemovingTheEmbeddingOnceSet(t *testing.T) {
	tests := []struct {
		name   string
		change func(*definition.Model)
	}{
		{"another name", func(m *definition.Model) { m.Embedding.Name = otherEmbeddingName }},
		{"another base URL", func(m *definition.Model) { m.Embedding.BaseURL = "https://other.example/v1" }},
		{"removed with the move to the mock", func(m *definition.Model) { m.Provider, m.Embedding = mockProvider, nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			// Revision 1, from the fixture's template, names config's embedding.
			f := registered(t)

			changed := f.configure("c1", "ego", 1)
			changed.Config = withModel(tt.change)
			_, err := f.service.Configure(ctx, changed)
			require.ErrorIs(t, err, definition.ErrEmbeddingImmutable)
			d, err := f.service.GetDefinition(ctx, firstAgent)
			require.NoError(t, err)
			assert.Equal(t, definition.Revision(1), d.Revision)

			// A request based on an earlier revision is stale first, whatever its embedding.
			_, err = f.service.Configure(ctx, f.configure("c2", "next", 1))
			require.NoError(t, err)
			stale := f.configure("c3", "ego", 1)
			stale.Config = withModel(tt.change)
			_, err = f.service.Configure(ctx, stale)
			require.ErrorIs(t, err, definition.ErrStaleRevision)

			// Control: changing only the embedding's key is accepted.
			rekeyed := f.configure("c4", "ego", 2)
			rekeyed.Config = withModel(func(m *definition.Model) { m.Embedding.APIKey = "embeddings/key" })
			_, err = f.service.Configure(ctx, rekeyed)
			require.NoError(t, err)
			d, err = f.service.GetDefinition(ctx, firstAgent)
			require.NoError(t, err)
			assert.Equal(t, definition.SecretRef("embeddings/key"), d.Config.Model.Embedding.APIKey)
		})
	}
}

func TestConfigure_ARevisionNamingNoEmbeddingConstrainsNone(t *testing.T) {
	ctx := context.Background()
	f := registeredOnTheMock(t)

	// Revision 1 names none, so any embedding may follow it.
	other := f.configure("c1", "ego", 1)
	other.Config = withModel(func(m *definition.Model) { m.Embedding.Name = otherEmbeddingName })
	_, err := f.service.Configure(ctx, other)
	require.NoError(t, err)

	// Control: from then on it is set, and another name is refused.
	again := f.configure("c2", "ego", 2)
	again.Config = withModel(func(*definition.Model) {})
	_, err = f.service.Configure(ctx, again)
	require.ErrorIs(t, err, definition.ErrEmbeddingImmutable)
}

func TestConfigure_AnotherOrganizationsAgentIsNotFoundBeforeItsEmbeddingIsCompared(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	_, err := f.service.PublishProfile(ctx, globex, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)
	changed := f.configure("c1", "ego", 1)
	changed.Config = withModel(func(m *definition.Model) { m.Embedding.Name = otherEmbeddingName })

	// firstAgent was created in org: globex learns nothing of its embedding.
	inGlobex := changed
	inGlobex.Request.Organization = globex
	_, err = f.service.Configure(ctx, inGlobex)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: in the agent's own organization, the same change is refused as one.
	_, err = f.service.Configure(ctx, changed)
	require.ErrorIs(t, err, definition.ErrEmbeddingImmutable)
}
