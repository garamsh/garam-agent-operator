//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// garam is the real garam the suite brought up, with one agent garam created for the test and
// assigned to the controller.
type garam struct {
	stack      *garamStack
	agentGRN   string
	assignment string
}

func (g garam) org() string   { return g.stack.orgID }
func (g garam) agent() string { return g.agentGRN }

// mint has garam mint an agent:configure authority for requestID and the exact body.
func (g garam) mint(t *testing.T, requestID string, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(body)
	authority, _, err := g.stack.mintAuthority("agent:configure", g.agentGRN, requestID, hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	return authority
}

// controllerClient presents the certificate garam issued the controller the agent is assigned to.
func (g garam) controllerClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: apiClient.Transport.(*http.Transport).TLSClientConfig.RootCAs, Certificates: []tls.Certificate{g.stack.controller},
	}}}
}

// requireGaram has the real garam create an agent for the test, assigned to the controller.
func requireGaram(t *testing.T) garam {
	t.Helper()
	agent, epoch, err := real.createAgent(name(t, "create"))
	require.NoError(t, err)
	return garam{stack: real, agentGRN: agent, assignment: epoch}
}

// seedRevision stores revision 1 of g's agent in the binary's database, as a creation would;
// no route creates an agent yet.
func seedRevision(t *testing.T, g garam) string {
	t.Helper()
	profile := publishProfile(t)
	require.NoError(t, execute(t, `WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, revision, profile_name, profile_version, config, position)
SELECT $1::text, 1, $2::text, 1, '{}', (SELECT position FROM next)`, g.agent(), profile))
	return profile
}

func configureBody(requestID, profile, ego string, expected int) []byte {
	b, err := json.Marshal(map[string]any{
		"requestId":        requestID,
		"expectedRevision": strconv.Itoa(expected),
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
	url := fmt.Sprintf("%s/v1/orgs/%s/agents/%s/revisions", attachedURL, g.org(), g.agent())
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
// concurrently sends each body under its authority at once, and returns each status.
func concurrently(t *testing.T, g garam, authorities []string, bodies [][]byte) []int {
	t.Helper()
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

	// Each round races concurrency configures on the revision the round before it stored. One
	// race may serialize on its own, so the suite runs many.
	for round := 1; round <= raceRounds; round++ {
		bodies := make([][]byte, concurrency)
		authorities := make([]string, concurrency)
		for i := range concurrency {
			requestID := name(t, "request")
			bodies[i] = configureBody(requestID, profile, fmt.Sprintf("round %d edit %d", round, i), round)
			authorities[i] = g.mint(t, requestID, bodies[i])
		}
		counts := map[int]int{}
		for _, status := range concurrently(t, g, authorities, bodies) {
			counts[status]++
		}
		require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: concurrency - 1}, counts, "round %d", round)
		require.Equal(t, round+1, revisionCount(t, g.agent()), "round %d", round)
	}
}

func TestConfigure_RepeatedRequestReturnsFirstOutcome(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)
	requestID := name(t, "request")
	body := configureBody(requestID, profile, "edited", 1)

	// Every repeat presents the one authority garam minted for the request, at once. garam
	// introspects without consuming, so each repeat is authorized and only the store decides.
	authority := g.mint(t, requestID, body)
	authorities := make([]string, concurrency)
	bodies := make([][]byte, concurrency)
	for i := range concurrency {
		authorities[i], bodies[i] = authority, body
	}
	for _, status := range concurrently(t, g, authorities, bodies) {
		assert.Equal(t, http.StatusOK, status)
	}
	assert.Equal(t, 2, revisionCount(t, g.agent()))
	assert.Equal(t, 1, requestCount(t, g.org(), requestID))

	// The console's retry: garam mints a fresh authority for the same request id and binding,
	// replacing the first, and the repeat under it is answered the first outcome.
	status, err := sendConfigure(g, g.mint(t, requestID, body), body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 2, revisionCount(t, g.agent()))

	// Control: the same change under another request id is a new request, and stale.
	other := name(t, "request")
	otherBody := configureBody(other, profile, "edited", 1)
	status, err = sendConfigure(g, g.mint(t, other, otherBody), otherBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status)
}

func requestCount(t *testing.T, org, requestID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM requests WHERE organization = $1 AND request_id = $2", org, requestID).Scan(&n))
	return n
}

const (
	// concurrency is how many requests race in one round.
	concurrency = 16
	// raceRounds is how many rounds the configure race runs, since any one round may serialize.
	raceRounds = 10
)

func revisionCount(t *testing.T, agent string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM definitions WHERE agent = $1", agent).Scan(&n))
	return n
}

func TestDesired_ReleasesAConfiguredAgentToItsController(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)
	requestID := name(t, "request")
	body := configureBody(requestID, profile, "edited", 1)
	status, err := sendConfigure(g, g.mint(t, requestID, body), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	// The controller garam assigned the agent to is released the revision just stored.
	resp, err := g.controllerClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var feed struct {
		Agents []struct {
			Agent    string `json:"agent"`
			Revision string `json:"revision"`
		} `json:"agents"`
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	released := map[string]string{}
	for _, a := range feed.Agents {
		released[a.Agent] = a.Revision
	}
	assert.Equal(t, "2", released[g.agent()], "the agent's configured revision is not in its controller's feed")
}
