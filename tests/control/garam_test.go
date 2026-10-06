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
func (g garam) controllerClient() *http.Client { return g.stack.feedClient() }

// feedClient presents the controller's certificate garam issued, trusting the binary's serving root.
func (s *garamStack) feedClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      apiClient.Transport.(*http.Transport).TLSClientConfig.RootCAs,
		Certificates: []tls.Certificate{s.controller},
	}}}
}

// requireGaram has the real garam create an agent for the test, assigned to the controller.
func requireGaram(t *testing.T) garam {
	t.Helper()
	agent, epoch, err := real.createAgent(name(t, "create"))
	require.NoError(t, err)
	return garam{stack: real, agentGRN: agent, assignment: epoch}
}

// seedRevision stores revision 1 of g's agent in the binary's database, as a creation in g's
// organization would; no route creates an agent yet.
func seedRevision(t *testing.T, g garam) string {
	t.Helper()
	profile := publishProfile(t, g.org())
	require.NoError(t, insertDefinition(t, g.agent(), g.org(), 1, profile))
	return profile
}

// configureRequest is the configure route's body, as the console sends it.
type configureRequest struct {
	RequestID        string `json:"requestId"`
	ExpectedRevision string `json:"expectedRevision"`
	Profile          struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	} `json:"profile"`
	Configuration struct {
		Model struct {
			Provider  string `json:"provider"`
			BaseURL   string `json:"baseUrl"`
			Name      string `json:"name"`
			APIKeyRef string `json:"apiKeyRef"`

			Embedding *configureEmbedding `json:"embedding,omitempty"`
		} `json:"model"`
		Ego   string            `json:"ego"`
		Tools map[string]string `json:"tools"`
	} `json:"configuration"`
}

// configureEmbedding is the configure route's configuration.model.embedding.
type configureEmbedding struct {
	BaseURL   string `json:"baseUrl"`
	Name      string `json:"name"`
	APIKeyRef string `json:"apiKeyRef,omitempty"`
}

func configureBody(requestID, profile, ego string, expected int) []byte {
	return newConfigureRequest(requestID, profile, ego, expected).body()
}

// configureBodyWithKey is configureBody with the model's key named by keyRef.
func configureBodyWithKey(requestID, profile, ego string, expected int, keyRef string) []byte {
	in := newConfigureRequest(requestID, profile, ego, expected)
	in.Configuration.Model.APIKeyRef = keyRef
	return in.body()
}

// newConfigureRequest is a configure request naming a model and its embeddings endpoint.
func newConfigureRequest(requestID, profile, ego string, expected int) configureRequest {
	var in configureRequest
	in.RequestID, in.ExpectedRevision = requestID, strconv.Itoa(expected)
	in.Profile.Name, in.Profile.Version = profile, 1
	in.Configuration.Model.Provider = "anthropic"
	in.Configuration.Model.BaseURL = "https://api.anthropic.com"
	in.Configuration.Model.Name = "claude-opus-5-5"
	in.Configuration.Model.APIKeyRef = "model-api-key/api-key"
	in.Configuration.Model.Embedding = &configureEmbedding{
		BaseURL: "https://embeddings.example/v1", Name: "bge-base-en-v1.5", APIKeyRef: "embeddings/key",
	}
	in.Configuration.Ego = ego
	in.Configuration.Tools = map[string]string{"web_fetch": "sha256:aa"}
	return in
}

func (in configureRequest) body() []byte {
	b, err := json.Marshal(in)
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

func TestConfigure_RefusesAMalformedKeyReferenceThroughGaram(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)
	requestID := name(t, "request")
	body := configureBodyWithKey(requestID, profile, "malformed", 1, "model-api-key")

	// garam authorizes the request; the binary refuses its configuration before storing it.
	url := fmt.Sprintf("%s/v1/orgs/%s/agents/%s/revisions", attachedURL, g.org(), g.agent())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+g.mint(t, requestID, body))
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var refusal struct {
		Kind string `json:"kind"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&refusal))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_api_key_ref", refusal.Kind)
	assert.Equal(t, 1, revisionCount(t, g.agent()))
	assert.Equal(t, 0, requestCount(t, g.org(), requestID))

	// Control: the same change with a well-formed reference, under its own request id, is stored.
	other := name(t, "request")
	sound := configureBodyWithKey(other, profile, "well formed", 1, "model-api-key/api-key")
	status, err := sendConfigure(g, g.mint(t, other, sound), sound)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, 2, revisionCount(t, g.agent()))
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
	assert.Equal(t, []string{"2"}, releasedRevisions(t, g),
		"the agent's configured revision is not in its controller's feed")
}

// releasedRevisions is every revision of g's agent its controller's feed releases, in the order released.
func releasedRevisions(t *testing.T, g garam) []string {
	t.Helper()
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
	var released []string
	for _, a := range feed.Agents {
		if a.Agent == g.agent() {
			released = append(released, a.Revision)
		}
	}
	return released
}

func TestConfigure_AnotherOrganizationsProfileAnswersNotFound(t *testing.T) {
	g := requireGaram(t)
	seedRevision(t, g)
	// Another organization publishes a profile name g's organization has not.
	profile := publishProfile(t, name(t, "organization"))
	requestID := name(t, "request")
	body := configureBody(requestID, profile, "edited", 1)

	status, err := sendConfigure(g, g.mint(t, requestID, body), body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, 1, revisionCount(t, g.agent()))

	// Control: once g's organization publishes the name, the same request is accepted, and its
	// controller is released the one revision it stored, beside the other organization's profile.
	require.NoError(t, insertProfile(t, g.org(), profile))
	status, err = sendConfigure(g, g.mint(t, requestID, body), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"2"}, releasedRevisions(t, g))
}

// publishTemplate stores version 1 of organization's template named for the test, over its profile,
// in the binary's database; no route publishes one yet.
func publishTemplate(t *testing.T, organization, profile string) string {
	t.Helper()
	template := name(t, "template")
	require.NoError(t, execute(t, `INSERT INTO templates
    (organization, name, version, profile_name, profile_version, config)
VALUES ($1, $2, 1, $3, 1, '{"ego":"created"}')`, organization, template, profile))
	return template
}

// createAgentBody is the console's create route's body for controller, template and profile.
func createAgentBody(requestID, controller, template, profile string) []byte {
	type ref struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	b, err := json.Marshal(struct {
		RequestID  string `json:"requestId"`
		Controller string `json:"controller"`
		Template   ref    `json:"template"`
		Profile    ref    `json:"profile"`
	}{requestID, controller, ref{template, 1}, ref{profile, 1}})
	if err != nil {
		panic(err)
	}
	return b
}

// createThroughConsole posts body to the attached binary's create route under authority.
func createThroughConsole(t *testing.T, authority string, body []byte) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, attachedURL+"/v1/orgs/"+real.orgID+"/agents", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]string{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// mintCreate has garam mint an agent:create authority on the controller for the exact body.
func mintCreate(requestID string, body []byte) (string, error) {
	digest := sha256.Sum256(body)
	authority, _, err := real.mintAuthority("agent:create", real.controllerGRN, requestID, hex.EncodeToString(digest[:]))
	return authority, err
}

func TestCreate_ThroughTheConsoleRouteAgainstGaram(t *testing.T) {
	profile := publishProfile(t, real.orgID)
	template := publishTemplate(t, real.orgID, profile)
	requestID := name(t, "create")
	body := createAgentBody(requestID, real.controllerGRN, template, profile)

	authority, err := mintCreate(requestID, body)
	require.NoError(t, err)
	status, first := createThroughConsole(t, authority, body)
	require.Equal(t, http.StatusCreated, status, first)
	require.NotEmpty(t, first["agent"])
	assert.Equal(t, "1", first["revision"])
	assert.NotEmpty(t, first["epoch"])

	// The console's retry: garam mints a fresh authority for the same request and body, and the
	// repeat, which asks garam again, is answered the same agent.
	retry, err := mintCreate(requestID, body)
	require.NoError(t, err)
	status, repeat := createThroughConsole(t, retry, body)
	assert.Equal(t, http.StatusOK, status, repeat)
	assert.Equal(t, first, repeat)

	// A changed body under the same request id: garam refuses to mint an authority for it, and the
	// authority it minted for the first body does not carry it either.
	changed := createAgentBody(requestID, real.controllerGRN, template, publishProfile(t, real.orgID))
	_, err = mintCreate(requestID, changed)
	require.ErrorContains(t, err, "answered 409")
	status, refused := createThroughConsole(t, retry, changed)
	assert.Equal(t, http.StatusForbidden, status, refused)
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM creations WHERE organization = $1 AND request_id = $2",
		real.orgID, requestID), "a second creation was stored")
	assert.Equal(t, 1, revisionCount(t, first["agent"]))

	// The created agent's revision 1 is released to the controller garam assigned it to.
	resp, err := real.feedClient().Get(attachedURL + "/v1/operators/self/desired")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var feed struct {
		Agents []struct {
			Agent    string `json:"agent"`
			Revision string `json:"revision"`
			Epoch    string `json:"epoch"`
		} `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&feed))
	released := map[string]string{}
	for _, a := range feed.Agents {
		released[a.Agent] = a.Revision + "@" + a.Epoch
	}
	assert.Equal(t, "1@"+first["epoch"], released[first["agent"]], "revision 1 is not in its controller's feed")
}

// count is the one integer query answers for arg.
func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestCreate_AnotherOrganizationsTemplateOrProfileAnswersNotFound(t *testing.T) {
	tests := []struct {
		name string
		// names publishes the template and profile the request names, one of them only in other,
		// and returns publish, which publishes that one in garam's organization too.
		names func(t *testing.T, other string) (template, profile string, publish func())
	}{
		{"another organization's template", func(t *testing.T, other string) (string, string, func()) {
			own := publishProfile(t, real.orgID)
			template := publishTemplate(t, other, publishProfile(t, other))
			return template, own, func() {
				require.NoError(t, execute(t, `INSERT INTO templates
    (organization, name, version, profile_name, profile_version, config)
VALUES ($1, $2, 1, $3, 1, '{"ego":"created"}')`, real.orgID, template, own))
			}
		}},
		{"another organization's profile", func(t *testing.T, other string) (string, string, func()) {
			template := publishTemplate(t, real.orgID, publishProfile(t, real.orgID))
			profile := publishProfile(t, other)
			return template, profile, func() { require.NoError(t, insertProfile(t, real.orgID, profile)) }
		}},
	}
	for _, tt := range tests {
		// The parent's name: a subtest's holds a '/', which garam's request ids refuse.
		requestID := name(t, "create")
		t.Run(tt.name, func(t *testing.T) {
			template, profile, publish := tt.names(t, name(t, "organization"))
			body := createAgentBody(requestID, real.controllerGRN, template, profile)

			authority, err := mintCreate(requestID, body)
			require.NoError(t, err)
			status, refused := createThroughConsole(t, authority, body)
			assert.Equal(t, http.StatusNotFound, status, refused)
			assert.Equal(t, 0, count(t, "SELECT count(*) FROM creations WHERE organization = $1 AND request_id = $2",
				real.orgID, requestID), "a refused creation was stored")

			// Control: once garam's organization publishes the name, the same request creates the agent.
			publish()
			retry, err := mintCreate(requestID, body)
			require.NoError(t, err)
			status, created := createThroughConsole(t, retry, body)
			assert.Equal(t, http.StatusCreated, status, created)
		})
	}
}
