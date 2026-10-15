package console_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// operatorEntry is one operator as the read answers it, last contacted at contact, with images
// and no reportedAt.
func operatorEntry(grn string, contact time.Time, images ...any) map[string]any {
	return map[string]any{"grn": grn, "images": append([]any{}, images...), "lastContactAt": contact.Format(time.RFC3339)}
}

// imageNameField is the field an image's name is answered under.
const imageNameField = "name"

// imageEntry is one image as the read answers it; an empty digest is none.
func imageEntry(name, ref, digest string) map[string]any {
	entry := map[string]any{imageNameField: name, "ref": ref}
	if digest != "" {
		entry["digest"] = digest
	}
	return entry
}

func TestOperators_AnswerEachControllerOfTheOrganizationAsReported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const (
		reported  = "grn:root:default:operator:reported"
		silent    = "grn:root:default:operator:silent"
		foreign   = "grn:root:default:operator:foreign"
		digest    = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		otherGRN  = "grn:root:default:org:" + otherOrg
		agentRef  = "registry.example/sherlock:1.2.0@" + digest
		copyImage = "busybox:stable-musl"
	)
	reportedAt := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	contactAt := reportedAt.Add(3 * time.Minute)
	sha := digest
	require.NoError(t, e.definitions.RecordOperatorContact(ctx, orgTarget, reported, contactAt))
	require.NoError(t, e.definitions.ReportOperatorImages(ctx, orgTarget, reported, []definition.OperatorImage{
		{Name: "agent", Ref: agentRef, Digest: &sha}, {Name: "copy", Ref: copyImage},
	}, reportedAt))
	require.NoError(t, e.definitions.RecordOperatorContact(ctx, orgTarget, silent, contactAt))
	require.NoError(t, e.definitions.RecordOperatorContact(ctx, otherGRN, foreign, contactAt))

	c := request{http.MethodGet, "/v1/orgs/" + org + "/operators", nil}
	got := e.send(t, c, e.bound(c, console.OperationOperatorRead, orgTarget, nil), nil)
	require.Equal(t, http.StatusOK, got.status, got.message)

	// The organization's own controllers, by GRN. One that never reported has no images and no
	// reportedAt, rather than an empty report made now; the other organization's is not listed.
	reportedEntry := operatorEntry(reported, contactAt, imageEntry("agent", agentRef, digest), imageEntry("copy", copyImage, ""))
	reportedEntry["reportedAt"] = reportedAt.Format(time.RFC3339)
	assert.Equal(t, []any{reportedEntry, operatorEntry(silent, contactAt)}, got.body["operators"])

	// The other organization's read lists its own controller and none of this one's.
	other := request{http.MethodGet, "/v1/orgs/" + otherOrg + "/operators", nil}
	a := e.bound(other, console.OperationOperatorRead, otherGRN, func(b *console.Binding) { b.Org = otherGRN })
	foreignRead := e.send(t, other, a, nil)
	require.Equal(t, http.StatusOK, foreignRead.status, foreignRead.message)
	assert.Equal(t, []any{operatorEntry(foreign, contactAt)}, foreignRead.body["operators"])

	// An authority of this organization does not read the other's, beside the one that does above.
	crossed := e.send(t, other, e.bound(other, console.OperationOperatorRead, otherGRN, nil), nil)
	assert.Equal(t, http.StatusForbidden, crossed.status)
}

func TestOperators_AnswerAnEmptyListWhereNoControllerReachedThisService(t *testing.T) {
	e := newEnv(t)
	c := request{http.MethodGet, "/v1/orgs/" + org + "/operators", nil}
	got := e.send(t, c, e.bound(c, console.OperationOperatorRead, orgTarget, nil), nil)
	require.Equal(t, http.StatusOK, got.status, got.message)
	assert.Equal(t, []any{}, got.body["operators"])
}
