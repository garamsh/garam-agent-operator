package console_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// malformedKeyReferences are key references the manager could not render, each failing one part
// of definition.SecretRef's rule.
var malformedKeyReferences = map[string]string{
	"no separator":             "model-api-key",
	"empty secret name":        "/api-key",
	"empty key":                "model-api-key/",
	"key holding a slash":      "model-api-key/api/key",
	"uppercase secret name":    "Model-Api-Key/api-key",
	"secret name ending in -":  "model-api-key-/api-key",
	"key with a space":         "model-api-key/api key",
	"key that is a dot-dot":    "model-api-key/..",
	"secret name with a colon": "model:api-key/api-key",
}

func TestConfigure_RefusesAMalformedKeyReference(t *testing.T) {
	for name, keyRef := range malformedKeyReferences {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)

			body := e.bodyWithKey("c1", "malformed", 1, keyRef)
			refused := e.configure(t, e.authorize("c1", body, nil), body)
			assert.Equal(t, 400, refused.status, refused.message)
			assert.Equal(t, "invalid_api_key_ref", refused.kind)
			assert.Equal(t, definition.Revision(1), e.revision(t).Revision, "nothing is stored")

			// Control: the same request with a well-formed reference, under a new request id, is
			// stored as the next revision with that reference.
			sound := e.bodyWithKey("c2", "well formed", 1, "model-api-key.v2/api_key-1")
			accepted := e.configure(t, e.authorize("c2", sound, nil), sound)
			require.Equal(t, 200, accepted.status, accepted.message)
			assert.Equal(t, definition.SecretRef("model-api-key.v2/api_key-1"), e.revision(t).Config.Model.APIKey)
		})
	}
}

func TestCreate_RefusesATemplateWithAMalformedKeyReference(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	configWithKey := func(keyRef string) definition.Configuration {
		return definition.Configuration{Model: definition.Model{
			Provider: "anthropic", BaseURL: "https://api.anthropic.com", Name: "claude-opus-5-5",
			APIKey: definition.SecretRef(keyRef),
		}}
	}

	// Stored straight into the repository: publishing refuses it now, so this stands for a
	// template published before the rule.
	broken, err := e.repository.PublishTemplate(ctx, org, definition.Template{
		Name: "broken", Profile: e.profile, Config: configWithKey("model-api-key"),
	})
	require.NoError(t, err)
	calls := e.registrar.calls

	body := e.createBody("broken-create", func(in *createRequest) {
		in.Template = ref{Name: broken.Name, Version: int64(broken.Version)}
	})
	refused := e.create(t, e.authorizeCreate("broken-create", body, nil), body)
	assert.Equal(t, 400, refused.status, refused.message)
	assert.Equal(t, "invalid_api_key_ref", refused.kind)
	assert.Equal(t, calls, e.registrar.calls, "garam is not asked")

	// Control: the same request id naming a template whose reference is well formed creates. Had
	// the refused request stored a creation, this one would be refused as a reused request id.
	sound, err := e.definitions.PublishTemplate(ctx, org, definition.Template{
		Name: "sound", Profile: e.profile, Config: configWithKey("model-api-key/api-key"),
	})
	require.NoError(t, err)
	soundBody := e.createBody("broken-create", func(in *createRequest) {
		in.Template = ref{Name: sound.Name, Version: int64(sound.Version)}
	})
	accepted := e.create(t, e.authorizeCreate("broken-create", soundBody, nil), soundBody)
	require.Equal(t, 201, accepted.status, accepted.message)
}
