//go:build e2e

package control_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// TestOperators_TheManagersReportIsReadThroughGaram reports the controller's images through the
// manager's own client, proved by garam, and reads them back with the controller's last contact
// under a real operator:read authority. Another organization's operator in the same store is not
// listed.
func TestOperators_TheManagersReportIsReadThroughGaram(t *testing.T) {
	before := time.Now().Add(-time.Second)
	pinned := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	client := desired.NewClient(strings.TrimPrefix(attachedURL, "https://"), real.feedTransport())
	require.NoError(t, client.ReportImages(context.Background(),
		desired.ConfiguredImages("registry.example/sherlock:1.2.0@"+pinned, "", "busybox:stable-musl", "")))

	foreign := name(t, "foreign-operator")
	_, err := pool.Exec(context.Background(),
		`INSERT INTO operators (grn, org_grn, last_contact_at) VALUES ($1, 'grn:root:default:org:elsewhere', now())`,
		foreign)
	require.NoError(t, err)

	path := "/v1/orgs/" + real.orgID + "/operators"
	status, answer := sendTargeted(t, http.MethodGet, path, "operator:read", real.orgGRN, name(t, "operators"), nil, path)
	require.Equal(t, http.StatusOK, status, answer)

	operators, ok := answer["operators"].([]any)
	require.True(t, ok, answer)
	var controller map[string]any
	for _, o := range operators {
		entry, _ := o.(map[string]any)
		assert.NotEqual(t, foreign, entry["grn"], "another organization's operator is listed")
		if entry["grn"] == real.controllerGRN {
			controller = entry
		}
	}
	require.NotNil(t, controller, "the controller is not listed: %v", answer)
	assert.Equal(t, []any{
		map[string]any{keyName: "agent", "ref": "registry.example/sherlock:1.2.0@" + pinned, "digest": pinned},
		map[string]any{keyName: "copy", "ref": "busybox:stable-musl"},
	}, controller["images"])
	for _, field := range []string{"reportedAt", "lastContactAt"} {
		value, _ := controller[field].(string)
		at, err := time.Parse(time.RFC3339Nano, value)
		require.NoError(t, err, "%s: %v", field, controller)
		assert.True(t, at.After(before), "%s %s is before the test began", field, value)
	}
}
