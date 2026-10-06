package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoRedirects_AnswersNotFoundWhereTheMuxWouldRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/v1/orgs/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server := httptest.NewServer(noRedirects(mux))
	t.Cleanup(server.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Control: a request on its route's canonical path is served.
	resp, err := client.Get(server.URL + "/v1/orgs/acme/templates")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// What the mux would redirect — a path it would clean, a subtree without its slash — names no route.
	for _, target := range []string{"/v1/orgs/acme//templates", "/v1/orgs/acme/../acme/templates", "/v1/orgs"} {
		req, err := http.NewRequest(http.MethodGet, server.URL+target, nil)
		require.NoError(t, err)
		req.URL.Opaque = target // sent as written, not cleaned by the client
		resp, err := client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, target)
		assert.Empty(t, resp.Header.Get("Location"), target)
	}
}

func TestConsoleOrigin_TakesOnlyAnExactOrigin(t *testing.T) {
	accepted := []string{"https://console.example.test", "https://console.example.test:8443", "http://localhost:3000"}
	for _, ok := range accepted {
		got, err := consoleOrigin(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, ok, got)
	}
	for _, refused := range []string{"https://console.example.test/", "https://console.example.test/garam",
		"https://*.example.test", "console.example.test", "ftp://console.example.test", "https://u:p@console.example.test",
		"https://console.example.test?x=1", "https://console.example.test#f", "", "https://Console.example.test"} {
		_, err := consoleOrigin(refused)
		assert.Error(t, err, refused)
	}
}
