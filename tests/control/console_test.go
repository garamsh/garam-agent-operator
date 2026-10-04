//go:build e2e

package control_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revisionsURL is the configure route for an agent of the organization acme.
func revisionsURL() string {
	return apiURL + "/v1/orgs/acme/agents/grn:acme:default:agent:0a1b2c3d/revisions"
}

func postConfigure(t *testing.T, authorization string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, revisionsURL(), bytes.NewReader([]byte(`{"requestId":"c1"}`)))
	require.NoError(t, err)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

func TestBinary_RefusesAConfigureWithoutAuthority(t *testing.T) {
	resp := postConfigure(t, "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Garam-Operation", resp.Header.Get("WWW-Authenticate"))

	// Control: a presented authority is taken to garam rather than refused at the door.
	presented := postConfigure(t, "Garam-Operation an-authority")
	assert.NotEqual(t, http.StatusUnauthorized, presented.StatusCode)
}

func TestBinary_AnswersUndecidedWhileGaramIsUnreachable(t *testing.T) {
	// Nothing answers at garamURL, so every bounded attempt at introspection fails to connect.
	resp := postConfigure(t, "Garam-Operation an-authority")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
