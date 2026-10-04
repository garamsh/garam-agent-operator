//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// garam is a real garam this suite obtains operation authorities from, for an organization,
// a user signed in to it, a delegation to operatorGRN and an agent assigned to a controller.
type garam interface {
	// org is the organization's identifier, the last segment of its GRN.
	org() string
	// agent is the GRN of the agent garam assigned.
	agent() string
	// mint has garam mint an agent:configure authority for requestID and the exact body.
	mint(t *testing.T, requestID string, body []byte) string
}

// requireGaram returns the garam this suite runs against, or skips the test. garam offers no
// supported way to sign a test user in and mint an authority for it: its only sign-in is an
// external OIDC provider, and seeding its database would bind this suite to garam's private
// schema (issue #230).
func requireGaram(t *testing.T) garam {
	t.Helper()
	t.Skip("needs a real garam that mints operation authorities for a test organization, " +
		"which garam has no supported bootstrap for yet (issue #230)")
	return nil
}

// seedRevision stores revision 1 of g's agent in the binary's database, as a creation would;
// no route creates an agent yet.
func seedRevision(t *testing.T, g garam) string {
	t.Helper()
	profile := publishProfile(t)
	require.NoError(t, execute(t, `INSERT INTO definitions (agent, revision, profile_name, profile_version, config)
VALUES ($1, 1, $2, 1, '{}')`, g.agent(), profile))
	return profile
}

func configureBody(requestID, profile, ego string, expected int) []byte {
	b, err := json.Marshal(map[string]any{
		"requestId":        requestID,
		"expectedRevision": expected,
		"profile":          map[string]any{"name": profile, "version": 1},
		"configuration": map[string]any{
			"model": map[string]string{"provider": "anthropic", "baseUrl": "https://api.anthropic.com",
				"name": "claude-opus-5-5", "apiKeyRef": "model-api-key"},
			"ego":   ego,
			"tools": map[string]string{"web_fetch": "sha256:aa"},
		},
	})
	if err != nil {
		panic(err)
	}
	return b
}

// sendConfigure posts body under authority to the binary's configure route for g's agent.
func sendConfigure(g garam, authority string, body []byte) (int, error) {
	url := fmt.Sprintf("%s/v1/orgs/%s/agents/%s/revisions", apiURL, g.org(), g.agent())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := apiClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// concurrently sends each of bodies under its own authority at once, and returns each status.
func concurrently(t *testing.T, g garam, requestIDs []string, bodies [][]byte) []int {
	t.Helper()
	authorities := make([]string, len(bodies))
	for i, body := range bodies {
		authorities[i] = g.mint(t, requestIDs[i], body)
	}
	statuses := make([]int, len(bodies))
	errs := make([]error, len(bodies))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Go(func() {
			<-start
			statuses[i], errs[i] = sendConfigure(g, authorities[i], bodies[i])
		})
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	return statuses
}

func TestConfigure_ConcurrentConfiguresOnOneRevisionStoreOne(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)

	requestIDs := make([]string, concurrency)
	bodies := make([][]byte, concurrency)
	for i := range concurrency {
		requestIDs[i] = name(t, "request")
		bodies[i] = configureBody(requestIDs[i], profile, fmt.Sprintf("edit %d", i), 1)
	}
	statuses := concurrently(t, g, requestIDs, bodies)

	counts := map[int]int{}
	for _, status := range statuses {
		counts[status]++
	}
	assert.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: concurrency - 1}, counts)
	assert.Equal(t, 2, revisionCount(t, g.agent()))
}

func TestConfigure_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)
	requestID := name(t, "request")
	body := configureBody(requestID, profile, "edited", 1)

	requestIDs := make([]string, concurrency)
	bodies := make([][]byte, concurrency)
	for i := range concurrency {
		requestIDs[i], bodies[i] = requestID, body
	}
	statuses := concurrently(t, g, requestIDs, bodies)

	for _, status := range statuses {
		assert.Equal(t, http.StatusOK, status)
	}
	assert.Equal(t, 2, revisionCount(t, g.agent()))
	var stored int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM requests WHERE organization = $1 AND request_id = $2", g.org(), requestID).Scan(&stored))
	assert.Equal(t, 1, stored)
}

// concurrency is how many requests race in one test.
const concurrency = 16

func revisionCount(t *testing.T, agent string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM definitions WHERE agent = $1", agent).Scan(&n))
	return n
}
