package console_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestConfigure_RefusesAModelWithNoEmbeddingAsEmbeddingRequired(t *testing.T) {
	for name, embedding := range map[string]*embeddingBody{
		"no embedding":                  nil,
		"an embedding with no base URL": {Name: embeddingName},
		"an embedding with no name":     {BaseURL: embeddingBaseURL},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.embedding = embedding
			body := e.body("c1", "ego", 1)
			refused := e.configure(t, e.authorize("c1", body, nil), body)
			assert.Equal(t, 400, refused.status, refused.message)
			assert.Equal(t, "embedding_required", refused.kind)
			assert.Equal(t, definition.Revision(1), e.revision(t).Revision, "nothing is stored")

			// Control: the same request id naming the whole embedding is stored, with it.
			e.embedding = anEmbedding()
			sound := e.body("c1", "ego", 1)
			accepted := e.configure(t, e.authorize("c1", sound, nil), sound)
			require.Equal(t, 200, accepted.status, accepted.message)
			assert.Equal(t, &definition.Embedding{
				BaseURL: embeddingBaseURL, Name: embeddingName, APIKey: "embeddings/key",
			}, e.revision(t).Config.Model.Embedding)
		})
	}
}

func TestConfigure_RefusesAMalformedEmbeddingKeyReference(t *testing.T) {
	e := newEnv(t)
	e.embedding.APIKeyRef = "embeddings"
	body := e.body("c1", "ego", 1)
	refused := e.configure(t, e.authorize("c1", body, nil), body)
	assert.Equal(t, 400, refused.status, refused.message)
	assert.Equal(t, "invalid_api_key_ref", refused.kind)

	// Control: an embedding taking no key is stored.
	e.embedding.APIKeyRef = ""
	sound := e.body("c1", "ego", 1)
	accepted := e.configure(t, e.authorize("c1", sound, nil), sound)
	require.Equal(t, 200, accepted.status, accepted.message)
	assert.Empty(t, e.revision(t).Config.Model.Embedding.APIKey)
}

func TestConfigure_RefusesChangingTheEmbeddingAsEmbeddingImmutable(t *testing.T) {
	e := newEnv(t)
	first := e.body("c1", "ego", 1)
	require.Equal(t, 200, e.configure(t, e.authorize("c1", first, nil), first).status)

	e.embedding.Name = "text-embedding-3-small"
	changed := e.body("c2", "ego", 2)
	refused := e.configure(t, e.authorize("c2", changed, nil), changed)
	assert.Equal(t, 409, refused.status, refused.message)
	assert.Equal(t, "embedding_immutable", refused.kind)
	assert.Equal(t, definition.Revision(2), e.revision(t).Revision, "nothing is stored")

	// Control: changing only the embedding's key is stored.
	e.embedding = anEmbedding()
	e.embedding.APIKeyRef = "other-embeddings/key"
	rekeyed := e.body("c3", "ego", 2)
	accepted := e.configure(t, e.authorize("c3", rekeyed, nil), rekeyed)
	require.Equal(t, 200, accepted.status, accepted.message)
	assert.Equal(t, definition.SecretRef("other-embeddings/key"), e.revision(t).Config.Model.Embedding.APIKey)
}

func TestCreate_RefusesATemplateWithNoEmbeddingAsEmbeddingRequired(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	model := definition.Model{
		Provider: modelProvider, BaseURL: modelBaseURL, Name: modelName, APIKey: "model-api-key/api-key",
	}
	// Stored straight into the repository: publishing refuses it now, so this stands for a
	// template published before the rule.
	missing, err := e.repository.PublishTemplate(ctx, org, definition.Template{
		Name: "missing", Profile: e.profile, Config: definition.Configuration{Model: model},
	})
	require.NoError(t, err)
	calls := e.registrar.calls

	body := e.createBody("create-1", func(in *createRequest) {
		in.Template = ref{Name: missing.Name, Version: int64(missing.Version)}
	})
	refused := e.create(t, e.authorizeCreate("create-1", body, nil), body)
	assert.Equal(t, 400, refused.status, refused.message)
	assert.Equal(t, "embedding_required", refused.kind)
	assert.Equal(t, calls, e.registrar.calls, "garam is not asked")

	// Control: the same request id naming a template whose model names its embedding creates.
	model.Embedding = &definition.Embedding{BaseURL: embeddingBaseURL, Name: embeddingName}
	sound, err := e.definitions.PublishTemplate(ctx, org, definition.Template{
		Name: "sound", Profile: e.profile, Config: definition.Configuration{Model: model},
	})
	require.NoError(t, err)
	soundBody := e.createBody("create-1", func(in *createRequest) {
		in.Template = ref{Name: sound.Name, Version: int64(sound.Version)}
	})
	accepted := e.create(t, e.authorizeCreate("create-1", soundBody, nil), soundBody)
	require.Equal(t, 201, accepted.status, accepted.message)
}
