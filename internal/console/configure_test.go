package console_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestConfigure_StaleRevisionRefused(t *testing.T) {
	e := newEnv(t)

	// Control: a request expecting the latest revision is stored as the next one.
	first := e.body("c1", "edited by one", 1)
	accepted := e.configure(t, e.authorize("c1", first, nil), first)
	require.Equal(t, 200, accepted.status, accepted.message)
	assert.Equal(t, "2", accepted.revision)

	second := e.body("c2", "edited by two", 1)
	refused := e.configure(t, e.authorize("c2", second, nil), second)
	assert.Equal(t, 409, refused.status, refused.message)

	assert.Equal(t, "edited by one", e.revision(t).Config.Ego)
}

func TestConfigure_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	e := newEnv(t)
	first := e.body("c1", "first", 1)
	accepted := e.configure(t, e.authorize("c1", first, nil), first)
	require.Equal(t, 200, accepted.status, accepted.message)
	next := e.body("c2", "second", 2)
	require.Equal(t, 200, e.configure(t, e.authorize("c2", next, nil), next).status)

	// garam mints a new authority for the same request id and binding when the console retries.
	repeat := e.configure(t, e.authorize("c1", first, nil), first)
	assert.Equal(t, accepted, repeat)
	assert.Equal(t, "second", e.revision(t).Config.Ego)
}

func TestConfigure_RequestIDReusedForAnotherBodyRefused(t *testing.T) {
	e := newEnv(t)
	first := e.body("c1", "first", 1)
	require.Equal(t, 200, e.configure(t, e.authorize("c1", first, nil), first).status)

	// An authority binding the other body's digest, under the request id already used.
	other := e.body("c1", "other", 1)
	refused := e.configure(t, e.authorize("c1", other, nil), other)
	assert.Equal(t, 409, refused.status, refused.message)

	// Control: the first body under the same request id is a repeat, not a reuse.
	repeat := e.configure(t, e.authorize("c1", first, nil), first)
	assert.Equal(t, 200, repeat.status, repeat.message)
	assert.Equal(t, "first", e.revision(t).Config.Ego)
}

func TestConfigure_AuthorityRefused(t *testing.T) {
	tests := []struct {
		name      string
		authority func(e *env, body []byte) console.Authority
		status    int
		challenge string
	}{
		{"none presented", func(*env, []byte) console.Authority { return "" }, 401, "Garam-Operation"},
		{"unknown to garam", func(*env, []byte) console.Authority { return "never-minted" }, 401, ""},
		{"expired by the clock", func(e *env, body []byte) console.Authority {
			return e.authorize("c1", body, func(b *console.Binding) { b.ExpiresAt = now })
		}, 401, ""},
		{"forbidden by garam", func(e *env, body []byte) console.Authority {
			a := e.authorize("c1", body, nil)
			e.introspector.set(a, console.Binding{}, console.ErrAuthorityForbidden)
			return a
		}, 403, ""},
		{"undecided by garam", func(e *env, body []byte) console.Authority {
			a := e.authorize("c1", body, nil)
			e.introspector.set(a, console.Binding{}, console.ErrAuthorityUndecided)
			return a
		}, 503, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body := e.body("c1", "edited", 1)

			refused := e.configure(t, tt.authority(e, body), body)
			assert.Equal(t, tt.status, refused.status, refused.message)
			assert.Equal(t, tt.challenge, refused.challenge)
			assert.Equal(t, "", e.revision(t).Config.Ego)

			// Control: the same body under a current authority is accepted, so the refusal stored nothing.
			accepted := e.configure(t, e.authorize("c1", body, nil), body)
			assert.Equal(t, 200, accepted.status, accepted.message)
		})
	}
}

func TestConfigure_RepeatUnderRefusedAuthorityRevealsNothing(t *testing.T) {
	e := newEnv(t)
	body := e.body("c1", "edited", 1)
	require.Equal(t, 200, e.configure(t, e.authorize("c1", body, nil), body).status)

	refused := e.configure(t, "never-minted", body)
	assert.Equal(t, 401, refused.status, refused.message)
	assert.Empty(t, refused.revision)

	// Control: the same repeat under a current authority is answered the stored outcome.
	repeat := e.configure(t, e.authorize("c1", body, nil), body)
	assert.Equal(t, 200, repeat.status, repeat.message)
	assert.Equal(t, "2", repeat.revision)
}

func TestConfigure_BoundFieldMismatchRefused(t *testing.T) {
	tests := []struct {
		name   string
		change func(*console.Binding)
	}{
		{"audience", func(b *console.Binding) { b.Audience = "grn:root:default:operator:other" }},
		{"organization", func(b *console.Binding) { b.Org = "grn:root:default:org:other" }},
		{"operation", func(b *console.Binding) { b.Operation = operationCreate }},
		{"target", func(b *console.Binding) { b.Target = "grn:acme:default:agent:9f2ac1b40d8e7a35" }},
		{"request id", func(b *console.Binding) { b.RequestID = "other" }},
		{"assignment", func(b *console.Binding) { b.Assignment = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body := e.body("c1", "edited", 1)

			refused := e.configure(t, e.authorize("c1", body, tt.change), body)
			assert.Equal(t, 403, refused.status, refused.message)
			assert.Contains(t, refused.message, tt.name)
			assert.Equal(t, "", e.revision(t).Config.Ego)

			// Control: an authority binding every field as the request carries it is accepted.
			accepted := e.configure(t, e.authorize("c1", body, nil), body)
			assert.Equal(t, 200, accepted.status, accepted.message)
		})
	}
}

func TestConfigure_BodyDigestMismatchRefused(t *testing.T) {
	e := newEnv(t)
	body := e.body("c1", "edited", 1)
	// The authority binds the digest of another body, otherwise the same request.
	other := e.body("c1", "authorized", 1)

	refused := e.configure(t, e.authorize("c1", other, nil), body)
	assert.Equal(t, 403, refused.status, refused.message)
	assert.Equal(t, "", e.revision(t).Config.Ego)

	// Control: an authority binding this body's digest is accepted.
	accepted := e.configure(t, e.authorize("c1", body, nil), body)
	assert.Equal(t, 200, accepted.status, accepted.message)
}

func TestConfigure_RefusesARevisionThatIsNotACanonicalString(t *testing.T) {
	for _, expected := range []string{`1`, `"01"`, `"0"`, `"one"`} {
		t.Run(expected, func(t *testing.T) {
			e := newEnv(t)
			body := []byte(`{"requestId":"c1","expectedRevision":` + expected +
				`,"profile":{"name":"small","version":1},"configuration":{"model":{},"ego":"","tools":{}}}`)
			refused := e.configure(t, e.authorize("c1", body, nil), body)
			assert.Equal(t, 400, refused.status, refused.message)

			// Control: the same body with a canonical expected revision is accepted.
			good := []byte(`{"requestId":"c1","expectedRevision":"1"` +
				`,"profile":{"name":"small","version":1},"configuration":{"model":{},"ego":"","tools":{}}}`)
			accepted := e.configure(t, e.authorize("c1", good, nil), good)
			assert.Equal(t, 200, accepted.status, accepted.message)
		})
	}
}

func TestConfigure_AnotherOrganizationsProfileAnswersNotFound(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	gpu, err := e.definitions.PublishProfile(ctx, "globex", "gpu", runnableSettings())
	require.NoError(t, err)
	e.profile = definition.ProfileRef{Name: gpu.Name, Version: gpu.Version}

	body := e.body("c1", "ego", 1)
	refused := e.configure(t, e.authorize("c1", body, nil), body)
	assert.Equal(t, 404, refused.status, refused.message)

	// The refusal is the one a name no organization published gets, so it discloses nothing.
	e.profile.Name = "unpublished"
	unpublished := e.body("c2", "ego", 1)
	never := e.configure(t, e.authorize("c2", unpublished, nil), unpublished)
	assert.Equal(t, 404, never.status, never.message)
	assert.Equal(t, strings.Replace(never.message, "unpublished", "gpu", 1), refused.message)

	// Control: once the agent's own organization publishes the name, the same request is accepted.
	_, err = e.definitions.PublishProfile(ctx, org, "gpu", runnableSettings())
	require.NoError(t, err)
	accepted := e.configure(t, e.authorize("c1", body, nil), body)
	assert.Equal(t, 200, accepted.status, accepted.message)
}
