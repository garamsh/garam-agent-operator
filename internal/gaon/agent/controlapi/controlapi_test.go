package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/agent/controlapi"
	"github.com/garamsh/garam-agent-operator/tests/gaon/gaonfake"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

var manager = grn.New("org1", "operator", "mgr1").String()

// tokens is a stand-in for the operator's token source.
type tokens struct {
	mu          sync.Mutex
	n           int
	invalidated int
}

func (s *tokens) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("garam_ot_%d", s.n), nil
}

func (s *tokens) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.invalidated++
}

// request is what the stand-in control service saw.
type request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// control is a stand-in control service. Its answer is a function a
// test replaces.
type control struct {
	mu     sync.Mutex
	seen   []request
	answer func(w http.ResponseWriter, r *http.Request)
	tokens *tokens
	client *controlapi.Client
}

func newControl(t *testing.T) *control {
	t.Helper()
	c := &control{tokens: &tokens{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.seen = append(c.seen, request{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), string(body)})
		answer := c.answer
		c.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := controlapi.New(controlapi.Options{
		URL: srv.URL, HTTP: srv.Client(), Tokens: c.tokens, Controller: manager, Wait: controlapi.MaxWait,
	})
	require.NoError(t, err)
	c.client = client
	return c
}

func (c *control) requests() []request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]request(nil), c.seen...)
}

func (c *control) reply(status int, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func agentJSON(id string, extra string) string {
	return fmt.Sprintf(`{"agent":%q,"revision":"2","epoch":"3",`+
		`"profile":{"name":"std","version":1,"resources":{},"storageSize":"1Gi","workspaceStorageSize":"10Gi"},`+
		`"configuration":{"model":{},"ego":"","tools":{}}%s}`,
		grn.New("org1", "agent", id).String(), extra)
}

func feedJSON(cursor string, agents ...string) string {
	return fmt.Sprintf(`{"cursor":%q,"agents":[%s]}`, cursor, strings.Join(agents, ","))
}

func TestDesiredReadsTheContractsAnswer(t *testing.T) {
	c := newControl(t)
	c.reply(200, feedJSON("7", agentJSON("a1", `,"stopped":true,"recovery":{"requestId":"r","epoch":"2"}`), agentJSON("a2", "")))
	got, err := c.client.Desired(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, grn.New("org1", "agent", "a1"), got[0].GRN)
	assert.Equal(t, int64(3), got[0].Epoch)
	assert.Equal(t, "2", got[0].Revision)
	assert.True(t, got[0].Stopped)
	assert.False(t, got[1].Stopped)
	assert.Equal(t, api.Quantity("1Gi"), got[0].MemoryStorage)
	assert.Equal(t, api.Quantity("10Gi"), got[0].WorkspaceStorage)
}

func TestDesiredFallsBackToTheMemorySizeForTheWorkspace(t *testing.T) {
	c := newControl(t)
	body := strings.Replace(agentJSON("a1", ""), `,"workspaceStorageSize":"10Gi"`, "", 1)
	c.reply(200, feedJSON("1", body))
	got, err := c.client.Desired(t.Context())
	require.NoError(t, err)
	assert.Equal(t, api.Quantity("1Gi"), got[0].WorkspaceStorage)
}

func TestDesiredSendsTheTokenTheControllerAndTheCursor(t *testing.T) {
	c := newControl(t)
	c.reply(200, feedJSON("7"))

	_, err := c.client.Desired(t.Context())
	require.NoError(t, err)
	first := c.requests()[0]
	assert.Equal(t, "GET", first.Method)
	assert.Equal(t, "/v1/operators/self/desired", first.Path)
	assert.Equal(t, "waitSeconds=30", first.Query, "no cursor is asked for on the first pass")
	assert.Equal(t, "Bearer garam_ot_0", first.Header.Get("Authorization"))
	assert.Equal(t, manager, first.Header.Get("Controller-Grn"))

	// A pass that was not settled asks for the same position again.
	_, err = c.client.Desired(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "waitSeconds=30", c.requests()[1].Query)

	// A settled pass moves the cursor to the answer's.
	c.client.Settle()
	_, err = c.client.Desired(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "after=7&waitSeconds=30", c.requests()[2].Query)
}

func TestAFailedPassDoesNotMoveTheCursor(t *testing.T) {
	c := newControl(t)
	c.reply(200, feedJSON("7"))
	_, err := c.client.Desired(t.Context())
	require.NoError(t, err)
	c.client.Settle()

	c.reply(200, feedJSON("9", agentJSON("a1", `,"bogus":1`)))
	_, err = c.client.Desired(t.Context())
	require.Error(t, err)
	c.client.Settle() // as the loop does only after a success; here it must stay at 7

	c.reply(200, feedJSON("9"))
	_, err = c.client.Desired(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "after=7&waitSeconds=30", c.requests()[2].Query)
}

func TestARefusedTokenIsExchangedAgainOnce(t *testing.T) {
	c := newControl(t)
	var n int
	c.mu.Lock()
	c.answer = func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, feedJSON("1"))
	}
	c.mu.Unlock()
	_, err := c.client.Desired(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, c.tokens.invalidated)
	reqs := c.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "Bearer garam_ot_1", reqs[1].Header.Get("Authorization"))

	// Refused twice, it is an error and not a loop.
	c.reply(401, `{"kind":"unauthenticated","message":"no"}`)
	_, err = c.client.Desired(t.Context())
	require.Error(t, err)
	assert.Equal(t, errorx.KindUnauthenticated, errorx.KindOf(err))
	assert.Len(t, c.requests(), 4)
}

func TestReportRenderedPostsTheRevisionToTheEscapedPath(t *testing.T) {
	c := newControl(t)
	c.reply(200, `{}`)
	g := grn.New("org1", "agent", "a1")
	require.NoError(t, c.client.ReportRendered(t.Context(), g, "4"))
	r := c.requests()[0]
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "/v1/operators/self/agents/"+strings.ReplaceAll(g.String(), "/", "%2F")+"/status", r.Path)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(r.Body), &body))
	assert.Equal(t, map[string]string{"observedRevision": "4", "renderedRevision": "4"}, body)
	assert.Equal(t, manager, r.Header.Get("Controller-Grn"))

	c.reply(500, `{}`)
	require.Error(t, c.client.ReportRendered(t.Context(), g, "4"))
	c.reply(204, ``)
	require.Error(t, c.client.ReportRendered(t.Context(), g, "4"), "only the contract's 200")
}

func TestRefusalsAreMapped(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   errorx.Kind
	}{
		{422, `{"kind":"too_many_agents","message":"501"}`, errorx.KindResourceExhausted},
		{403, `{}`, errorx.KindPermissionDenied},
		{404, `{}`, errorx.KindFailedPrecondition},
		{503, ``, errorx.KindUnavailable},
	}
	for _, tc := range cases {
		c := newControl(t)
		c.reply(tc.status, tc.body)
		_, err := c.client.Desired(t.Context())
		require.Error(t, err)
		assert.Equal(t, tc.kind, errorx.KindOf(err), "status %d", tc.status)
	}
}

func TestNewRefusesAWaitPastTheContractsCeiling(t *testing.T) {
	_, err := controlapi.New(controlapi.Options{Wait: controlapi.MaxWait + time.Second})
	require.Error(t, err)
}

// unreadable is every feed answer that is not a whole one.
var unreadable = []struct {
	name   string
	status int
	body   string
	// declared is a Content-Length longer than the body, which is how
	// a connection cut mid-answer reads.
	declared int
}{
	{name: "truncated by the connection", status: 200, body: feedJSON("2", agentJSON("a1", ""))[:60], declared: 400},
	{name: "cut json", status: 200, body: feedJSON("2", agentJSON("a1", ""))[:60]},
	{name: "empty body", status: 200, body: ``},
	{name: "array", status: 200, body: `[]`},
	{name: "empty object", status: 200, body: `{}`},
	{name: "null", status: 200, body: `null`},
	{name: "agents null", status: 200, body: `{"cursor":"2","agents":null}`},
	{name: "agents absent", status: 200, body: `{"cursor":"2"}`},
	{name: "cursor absent", status: 200, body: `{"agents":[]}`},
	{name: "cursor empty", status: 200, body: `{"cursor":"","agents":[]}`},
	{name: "trailing data", status: 200, body: feedJSON("2") + `{}`},
	{name: "unknown top-level field", status: 200, body: `{"cursor":"2","agents":[],"extra":1}`},
	{name: "unknown agent field", status: 200, body: feedJSON("2", agentJSON("a1", `,"extra":1`))},
	{name: "agent without profile", status: 200, body: feedJSON("2", `{"agent":"x","revision":"1","epoch":"1","configuration":{}}`)},
	{name: "revision with a leading zero", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), `"revision":"2"`, `"revision":"02"`, 1))},
	{name: "revision a number", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), `"revision":"2"`, `"revision":2`, 1))},
	{name: "epoch signed", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), `"epoch":"3"`, `"epoch":"+3"`, 1))},
	{name: "agent not a GRN", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), grn.New("org1", "agent", "a1").String(), "nope", 1))},
	{name: "agent of another type", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), grn.New("org1", "agent", "a1").String(), grn.New("org1", "operator", "a1").String(), 1))},
	{name: "agent named twice", status: 200, body: feedJSON("2", agentJSON("a1", ""), agentJSON("a1", ""))},
	{name: "bad storage quantity", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), `"storageSize":"1Gi"`, `"storageSize":"lots"`, 1))},
	{name: "configuration not an object", status: 200, body: feedJSON("2", strings.Replace(agentJSON("a1", ""), `"configuration":{"model":{},"ego":"","tools":{}}`, `"configuration":[]`, 1))},
	{name: "recovery without its fields", status: 200, body: feedJSON("2", agentJSON("a1", `,"recovery":{}`))},
	{name: "202 with a whole body", status: 202, body: feedJSON("2")},
	{name: "204", status: 204, body: ``},
	{name: "201 with a whole body", status: 201, body: feedJSON("2")},
	{name: "500", status: 500, body: `{"kind":"internal","message":"x"}`},
	{name: "503", status: 503, body: ``},
	{name: "422 too many agents", status: 422, body: `{"kind":"too_many_agents","message":"x"}`},
}

func name(g grn.GRN) string {
	sum := sha256.Sum256([]byte(g.String()))
	return "agent-" + hex.EncodeToString(sum[:])[:16]
}

type stubGaram struct{}

func (stubGaram) MintTokens(context.Context, grn.GRN, int64) (agent.Credentials, error) {
	return agent.Credentials{"agent-token": []byte("a"), "control-token": []byte("c")}, nil
}

func (stubGaram) ReportState(context.Context, grn.GRN, int64, agent.State) error { return nil }

// TestAnUnreadableAnswerFailsThePassAndHaltsNothing runs the real
// reconciler over the real feed client: an agent is created from a good
// answer, then each unreadable answer must fail the pass and leave the
// MicroVM running. The control is a well-formed empty set, which halts.
func TestAnUnreadableAnswerFailsThePassAndHaltsNothing(t *testing.T) {
	for _, tc := range unreadable {
		t.Run(tc.name, func(t *testing.T) {
			c := newControl(t)
			gaon, cluster := gaonfake.New(t, agent.Selector())
			svc := agent.NewService(c.client, stubGaram{}, c.client, cluster, agent.Options{
				Image: "registry.example/agent:1", GaramRoot: []byte("g"), ControlRoot: []byte("c"),
				Logger: slog.New(slog.DiscardHandler),
			})
			g := grn.New("org1", "agent", "a1")

			c.reply(200, feedJSON("1", agentJSON("a1", "")))
			require.NoError(t, svc.Reconcile(t.Context()))
			m, ok := gaon.MicroVM(t, name(g))
			require.True(t, ok)
			require.Equal(t, api.RunAlways, m.Spec.RunStrategy)

			c.mu.Lock()
			c.answer = func(w http.ResponseWriter, _ *http.Request) {
				if tc.declared > 0 {
					w.Header().Set("Content-Length", fmt.Sprint(tc.declared))
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}
			c.mu.Unlock()
			require.Error(t, svc.Reconcile(t.Context()))
			m, ok = gaon.MicroVM(t, name(g))
			require.True(t, ok)
			assert.Equal(t, api.RunAlways, m.Spec.RunStrategy, "nothing is halted by an answer that cannot be read")
			assert.Zero(t, gaon.Count("DELETE", "microvms"))
		})
	}
}

func TestAWellFormedEmptySetHaltsEveryAgent(t *testing.T) {
	c := newControl(t)
	gaon, cluster := gaonfake.New(t, agent.Selector())
	svc := agent.NewService(c.client, stubGaram{}, c.client, cluster, agent.Options{
		Image: "registry.example/agent:1", GaramRoot: []byte("g"), ControlRoot: []byte("c"),
		Logger: slog.New(slog.DiscardHandler),
	})
	g := grn.New("org1", "agent", "a1")
	c.reply(200, feedJSON("1", agentJSON("a1", "")))
	require.NoError(t, svc.Reconcile(t.Context()))

	c.reply(200, feedJSON("2"))
	require.NoError(t, svc.Reconcile(t.Context()))
	m, ok := gaon.MicroVM(t, name(g))
	require.True(t, ok)
	assert.Equal(t, api.RunHalted, m.Spec.RunStrategy)
}
