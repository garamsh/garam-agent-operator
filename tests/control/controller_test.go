//go:build e2e

package control_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func controllerRequest(t *testing.T, client *http.Client, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, apiURL+path, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestBinary_ControllerRoutesRequireAClientCertificate(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/operators/self/desired", ""},
		{http.MethodPost, "/v1/operators/self/agents/grn:acme:default:agent:a/status",
			`{"observedRevision":1,"renderedRevision":1}`},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			assert.Equal(t, http.StatusUnauthorized, controllerRequest(t, apiClient, route.method, route.path, route.body))

			// Control: a client certificate passes the door and reaches garam's proof, which
			// nothing answers here, so the request is undecided rather than refused.
			assert.Equal(t, http.StatusServiceUnavailable,
				controllerRequest(t, controllerClient, route.method, route.path, route.body))
		})
	}
}

func TestBinary_ConsoleRouteNeedsNoClientCertificate(t *testing.T) {
	// The console's route is reached without a client certificate, and answers its own refusal.
	resp := postConfigure(t, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Garam-Operation", resp.Header.Get("WWW-Authenticate"))
}
