//go:build e2e

package control_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOperators_TheControllersReportIsReadThroughGaram reports the controller's images under the
// token the controller exchanged, proved by garam, and reads them back with the controller's last contact
// under a real iam:operator.read authority. Another organization's operator in the same store is not
// listed.
func TestOperators_TheControllersReportIsReadThroughGaram(t *testing.T) {
	before := time.Now().Add(-time.Second)
	pinned := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	report := fmt.Sprintf(`{"images":[{"name":"agent","ref":"registry.example/sherlock:1.2.0@%[1]s","digest":"%[1]s"},`+
		`{"name":"copy","ref":"busybox:stable-musl"}]}`, pinned)
	put, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		attachedURL+"/v1/operators/self/images", strings.NewReader(report))
	require.NoError(t, err)
	put.Header.Set("Content-Type", "application/json")
	resp, err := real.feedClient().Do(put)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	foreign := name(t, "foreign-operator")
	_, err = pool.Exec(context.Background(),
		`INSERT INTO operators (grn, org_grn, last_contact_at) VALUES ($1, 'grn:root/iam/org/elsewhere', now())`,
		foreign)
	require.NoError(t, err)

	path := "/v1/orgs/" + real.orgID + "/operators"
	status, answer := sendTargeted(t, http.MethodGet, path, "iam:operator.read", real.orgGRN,
		name(t, "operators"), nil, path)
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
