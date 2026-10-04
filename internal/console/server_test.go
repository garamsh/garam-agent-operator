package console_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

const (
	audience = "grn:root:default:operator:control"
	org      = "acme"
	agent    = "grn:acme:default:agent:0a1b2c3d4e5f6071"
	actor    = "grn:acme:default:user:7c1d"
)

// now is the clock every test's server reads.
var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// answer is what the test double answers for one authority.
type answer struct {
	binding console.Binding
	err     error
}

// introspector is the test double for garam: it answers each authority as the test set it.
type introspector struct {
	mu      sync.Mutex
	answers map[console.Authority]answer
}

func (i *introspector) Introspect(_ context.Context, a console.Authority) (console.Binding, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	got, ok := i.answers[a]
	if !ok {
		return console.Binding{}, console.ErrAuthorityUnknown
	}
	return got.binding, got.err
}

func (i *introspector) set(a console.Authority, b console.Binding, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.answers[a] = answer{binding: b, err: err}
}

type registrar struct{}

func (registrar) Register(context.Context, definition.RequestKey) (definition.GRN, error) {
	return agent, nil
}

// env is a console server over an in-memory store holding agent at revision 1.
type env struct {
	url          string
	introspector *introspector
	definitions  definition.Service
	profile      definition.ProfileRef
	authorities  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	definitions := definition.NewService(repository.NewMemory(), registrar{})
	p, err := definitions.PublishProfile(ctx, "small", definition.ExecutionSettings{})
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}
	tmpl, err := definitions.PublishTemplate(ctx, definition.Template{Name: "researcher", Profile: profile})
	require.NoError(t, err)
	_, err = definitions.CreateAgent(ctx, definition.RequestKey{Organization: org, RequestID: "create"}, actor,
		definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version})
	require.NoError(t, err)
	d, err := definitions.GetDefinition(ctx, agent)
	require.NoError(t, err)
	require.Equal(t, definition.Revision(1), d.Revision)

	e := &env{introspector: &introspector{answers: map[console.Authority]answer{}}, definitions: definitions, profile: profile}
	server := httptest.NewServer(console.NewHandler(console.Config{
		Definitions:  definitions,
		Introspector: e.introspector,
		Audience:     audience,
		Now:          func() time.Time { return now },
		Logger:       slog.New(slog.DiscardHandler),
	}))
	t.Cleanup(server.Close)
	e.url = server.URL
	return e
}

// body is a configure request's body.
func (e *env) body(requestID, ego string, expected int) []byte {
	b, err := json.Marshal(map[string]any{
		"requestId":        requestID,
		"expectedRevision": expected,
		"profile":          map[string]any{"name": e.profile.Name, "version": e.profile.Version},
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

// authorize registers a new authority binding what garam would bind for body, changed by change.
func (e *env) authorize(requestID string, body []byte, change func(*console.Binding)) console.Authority {
	e.authorities++
	a := console.Authority(fmt.Sprintf("authority-%d", e.authorities))
	digest := sha256.Sum256(body)
	b := console.Binding{
		OperationRef: "ref-" + requestID,
		GrantID:      "grant-1",
		Org:          "grn:root:default:org:" + org,
		Actor:        actor,
		Audience:     audience,
		Operation:    console.OperationConfigure,
		Target:       agent,
		Assignment:   &console.Assignment{Operator: "grn:acme:default:operator:k8s", Epoch: "7"},
		RequestID:    requestID,
		BodySHA256:   hex.EncodeToString(digest[:]),
		ExpiresAt:    now.Add(5 * time.Minute),
	}
	if change != nil {
		change(&b)
	}
	e.introspector.set(a, b, nil)
	return a
}

// response is what a configure request was answered.
type response struct {
	status    int
	revision  int64
	message   string
	challenge string
}

// configure sends body under authority to agent's revisions route; an empty authority sends none.
func (e *env) configure(t *testing.T, authority console.Authority, body []byte) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url+"/v1/orgs/"+org+"/agents/"+agent+"/revisions", bytes.NewReader(body))
	require.NoError(t, err)
	if authority != "" {
		req.Header.Set("Authorization", "Garam-Operation "+string(authority))
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Revision int64  `json:"revision"`
		Message  string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return response{status: resp.StatusCode, revision: out.Revision, message: out.Message,
		challenge: resp.Header.Get("WWW-Authenticate")}
}

// revision is the agent's latest stored revision.
func (e *env) revision(t *testing.T) definition.Definition {
	t.Helper()
	d, err := e.definitions.GetDefinition(context.Background(), agent)
	require.NoError(t, err)
	return d
}
