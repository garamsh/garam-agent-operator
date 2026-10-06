package console_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// otherOrg is an organization beside org, whose names the console's org must never reach.
const otherOrg = "globex"

// templateName is the template the tests publish, keyRef the model key reference it names, and
// operationCreate the create handoff, which no route here accepts.
const (
	templateName    = "web"
	keyRef          = "model-keys/web"
	operationCreate = "agent:create"
	originHeader    = "Origin"
)

// orgTarget is the GRN the organization's reads and publication target.
const orgTarget = "grn:root:default:org:" + org

// request is one request to the console: its method, its exact request target, and its body.
type request struct {
	method string
	target string
	body   []byte
}

// reply is what a request was answered.
type reply struct {
	status  int
	header  http.Header
	body    map[string]any
	message string
}

// bound registers an authority binding what garam would bind for c under operation on target,
// changed by change.
func (e *env) bound(c request, operation, target string, change func(*console.Binding)) console.Authority {
	e.authorities++
	a := console.Authority(fmt.Sprintf("authority-%d", e.authorities))
	digest := sha256.Sum256(c.body)
	b := console.Binding{
		OperationRef:  "ref-" + string(a),
		GrantID:       "grant-1",
		Org:           orgTarget,
		Actor:         actor,
		Audience:      audience,
		Operation:     operation,
		Target:        target,
		RequestID:     "req-" + string(a),
		BodySHA256:    hex.EncodeToString(digest[:]),
		RequestTarget: c.target,
		ExpiresAt:     now.Add(5 * time.Minute),
	}
	if operation == console.OperationExecutionRead || operation == console.OperationConfigure {
		b.Assignment = &console.Assignment{Operator: "grn:acme:default:operator:k8s", Epoch: "7"}
	}
	if change != nil {
		change(&b)
	}
	e.introspector.set(a, b, nil)
	return a
}

// send makes c under authority, with the headers given.
func (e *env) send(t *testing.T, c request, authority console.Authority, headers map[string]string) reply {
	t.Helper()
	req, err := http.NewRequest(c.method, e.url+c.target, bytes.NewReader(c.body))
	require.NoError(t, err)
	if authority != "" {
		req.Header.Set("Authorization", "Garam-Operation "+string(authority))
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := reply{status: resp.StatusCode, header: resp.Header}
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out.body), string(raw))
		out.message, _ = out.body["message"].(string)
	}
	return out
}

// publishBody is a publish request's body, naming the env's profile.
func (e *env) publishBody(requestID, ego string) []byte {
	return publishBodyOf(requestID, versionOf(e.profile.Name, int64(e.profile.Version)), testConfiguration(ego, keyRef))
}

// publishBodyOf is a publish request's body.
func publishBodyOf(requestID string, profile, configuration map[string]any) []byte {
	b, err := json.Marshal(map[string]any{"requestId": requestID, "profile": profile, "configuration": configuration})
	if err != nil {
		panic(err)
	}
	return b
}

// publishAs makes a publish request with body under an authority binding the request id it names.
func (e *env) publishAs(t *testing.T, requestID string, body []byte, change func(*console.Binding)) reply {
	t.Helper()
	c := request{http.MethodPost, "/v1/orgs/" + org + "/templates/" + templateName + "/versions", body}
	a := e.bound(c, console.OperationTemplatePublish, orgTarget, func(b *console.Binding) {
		// garam mints a fresh authority for a repeat of one request under the same reference.
		b.RequestID, b.OperationRef = requestID, "ref-"+requestID
		if change != nil {
			change(b)
		}
	})
	return e.send(t, c, a, nil)
}

// reads are every read route against the env's own data, with the operation and target each binds.
func (e *env) reads() []struct {
	name      string
	call      request
	operation string
	target    string
} {
	return []struct {
		name      string
		call      request
		operation string
		target    string
	}{
		{"templates", request{http.MethodGet, "/v1/orgs/" + org + "/templates", nil}, console.OperationTemplateRead, orgTarget},
		{"template version", request{http.MethodGet, "/v1/orgs/" + org + "/templates/researcher/versions/1", nil},
			console.OperationTemplateRead, orgTarget},
		{"profiles", request{http.MethodGet, "/v1/orgs/" + org + "/profiles", nil}, console.OperationProfileRead, orgTarget},
		{"profile version", request{http.MethodGet, "/v1/orgs/" + org + "/profiles/small/versions/1", nil},
			console.OperationProfileRead, orgTarget},
		{"execution", request{http.MethodGet, "/v1/orgs/" + org + "/agents/" + agent + "/execution", nil},
			console.OperationExecutionRead, agent},
	}
}

func TestReads_RefuseAnAuthorityForAnotherOperation(t *testing.T) {
	e := newEnv(t)
	for _, read := range e.reads() {
		// Control: the operation the route binds is answered.
		accepted := e.send(t, read.call, e.bound(read.call, read.operation, read.target, nil), nil)
		require.Equal(t, http.StatusOK, accepted.status, "%s: %s", read.name, accepted.message)

		// A create or configure handoff, and the other reads' operations, are refused.
		for _, other := range []string{operationCreate, console.OperationConfigure, console.OperationTemplateRead,
			console.OperationTemplatePublish, console.OperationProfileRead, console.OperationExecutionRead} {
			if other == read.operation {
				continue
			}
			refused := e.send(t, read.call, e.bound(read.call, other, read.target, nil), nil)
			assert.Equal(t, http.StatusForbidden, refused.status, "%s under %s", read.name, other)
			assert.Equal(t, "operation authority binds another operation", refused.message, read.name)
		}
	}
}

func TestReads_RefuseAnAuthorityForAnotherRequestTarget(t *testing.T) {
	e := newEnv(t)
	for _, read := range e.reads() {
		// Control: the target the request arrived with is answered.
		accepted := e.send(t, read.call, e.bound(read.call, read.operation, read.target, nil), nil)
		require.Equal(t, http.StatusOK, accepted.status, "%s: %s", read.name, accepted.message)

		for _, bound := range []string{
			read.call.target + "?page=2", // the same path with a query the request does not carry
			"/prefix" + read.call.target, // a path prefix the request did not arrive under
			"",                           // no target bound at all
		} {
			a := e.bound(read.call, read.operation, read.target, func(b *console.Binding) { b.RequestTarget = bound })
			refused := e.send(t, read.call, a, nil)
			assert.Equal(t, http.StatusForbidden, refused.status, "%s bound to %q", read.name, bound)
			assert.Equal(t, "operation authority binds another request target", refused.message)
		}

		// The request carries a query the authority did not bind.
		queried := read.call
		queried.target += "?page=2"
		a := e.bound(read.call, read.operation, read.target, nil)
		assert.Equal(t, http.StatusForbidden, e.send(t, queried, a, nil).status, read.name)
	}
}

func TestReads_RefuseAnAuthorityForAnotherTargetOrBody(t *testing.T) {
	e := newEnv(t)
	for _, read := range e.reads() {
		accepted := e.send(t, read.call, e.bound(read.call, read.operation, read.target, nil), nil)
		require.Equal(t, http.StatusOK, accepted.status, "%s: %s", read.name, accepted.message)

		target := e.bound(read.call, read.operation, read.target+"x", nil)
		assert.Equal(t, http.StatusForbidden, e.send(t, read.call, target, nil).status, read.name)

		// A GET binds the empty body's digest; one binding any other body's is refused.
		other := read.call
		other.body = []byte("{}")
		digest := e.bound(other, read.operation, read.target, nil)
		refused := e.send(t, read.call, digest, nil)
		assert.Equal(t, http.StatusForbidden, refused.status, read.name)
		assert.Equal(t, console.ErrDigestMismatch.Error(), refused.message, read.name)

		// The execution read binds where the agent runs, as configure does.
		if read.operation == console.OperationExecutionRead {
			unassigned := e.bound(read.call, read.operation, read.target, func(b *console.Binding) { b.Assignment = nil })
			assert.Equal(t, http.StatusForbidden, e.send(t, read.call, unassigned, nil).status)
		}
	}
}

func TestReads_ResolveOnlyInTheRequestsOrganization(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	foreign, err := e.definitions.PublishProfile(ctx, otherOrg, "foreign", definition.ExecutionSettings{})
	require.NoError(t, err)
	_, err = e.definitions.PublishTemplate(ctx, otherOrg, definition.Template{Name: "foreign",
		Profile: definition.ProfileRef{Name: foreign.Name, Version: foreign.Version}})
	require.NoError(t, err)

	get := func(target, operation, grn string) reply {
		c := request{http.MethodGet, target, nil}
		return e.send(t, c, e.bound(c, operation, grn, nil), nil)
	}

	// Control: the organization's own names are found.
	assert.Equal(t, http.StatusOK, get("/v1/orgs/"+org+"/templates/researcher/versions/1", console.OperationTemplateRead, orgTarget).status)
	assert.Equal(t, http.StatusOK, get("/v1/orgs/"+org+"/profiles/small/versions/1", console.OperationProfileRead, orgTarget).status)

	// Another organization's names are not found, as names nobody published are not.
	assert.Equal(t, http.StatusNotFound, get("/v1/orgs/"+org+"/templates/foreign/versions/1", console.OperationTemplateRead, orgTarget).status)
	assert.Equal(t, http.StatusNotFound, get("/v1/orgs/"+org+"/profiles/foreign/versions/1", console.OperationProfileRead, orgTarget).status)
	templates := get("/v1/orgs/"+org+"/templates", console.OperationTemplateRead, orgTarget)
	listed := answered("researcher", 1)
	listed["profile"] = answered(e.profile.Name, 1)
	assert.Equal(t, []any{listed}, templates.body["templates"])
	profiles := get("/v1/orgs/"+org+"/profiles", console.OperationProfileRead, orgTarget)
	assert.Equal(t, []any{answered(e.profile.Name, 1)}, profiles.body["profiles"])

	// An agent another organization's creation made is not found either.
	c := request{http.MethodGet, "/v1/orgs/" + otherOrg + "/agents/" + agent + "/execution", nil}
	a := e.bound(c, console.OperationExecutionRead, agent, func(b *console.Binding) { b.Org = "grn:root:default:org:" + otherOrg })
	assert.Equal(t, http.StatusNotFound, e.send(t, c, a, nil).status)
}

func TestTemplateVersion_AnswersTheKeyReferenceAndNoValue(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, http.StatusCreated, e.publishAs(t, "p1", e.publishBody("p1", "answers web"), nil).status)

	c := request{http.MethodGet, "/v1/orgs/" + org + "/templates/" + templateName + "/versions/1", nil}
	got := e.send(t, c, e.bound(c, console.OperationTemplateRead, orgTarget, nil), nil)
	require.Equal(t, http.StatusOK, got.status, got.message)
	want := answered(templateName, 1)
	want["profile"] = answered(e.profile.Name, 1)
	want["configuration"] = decoded(t, testConfiguration("answers web", keyRef))
	assert.Equal(t, want, got.body)

	// A version the template does not have, or one that is no canonical number, names nothing.
	for _, v := range []string{"2", "01", "x"} {
		missing := request{http.MethodGet, "/v1/orgs/" + org + "/templates/" + templateName + "/versions/" + v, nil}
		assert.Equal(t, http.StatusNotFound,
			e.send(t, missing, e.bound(missing, console.OperationTemplateRead, orgTarget, nil), nil).status, v)
	}
}

func TestPublish_PublishesTheNextVersionOncePerRequest(t *testing.T) {
	e := newEnv(t)
	body := e.publishBody("p1", "first")

	first := e.publishAs(t, "p1", body, nil)
	require.Equal(t, http.StatusCreated, first.status, first.message)
	assert.Equal(t, answered(templateName, 1), first.body)

	// An identical repeat, under a newly minted authority, answers the same version and publishes none.
	repeat := e.publishAs(t, "p1", body, nil)
	assert.Equal(t, http.StatusOK, repeat.status, repeat.message)
	assert.Equal(t, first.body, repeat.body)

	// Control: a new request publishes the next version, and never edits the first.
	second := e.publishAs(t, "p2", e.publishBody("p2", "second"), nil)
	assert.Equal(t, answered(templateName, 2), second.body)
	v1, err := e.definitions.GetTemplate(context.Background(), org, definition.TemplateRef{Name: templateName, Version: 1})
	require.NoError(t, err)
	assert.Equal(t, "first", v1.Config.Ego)

	// The request id reused for another body is refused, and publishes nothing.
	reused := e.publishAs(t, "p1", e.publishBody("p1", "other"), nil)
	assert.Equal(t, http.StatusConflict, reused.status, reused.message)
	latest, err := e.definitions.ListTemplates(context.Background(), org)
	require.NoError(t, err)
	assert.Equal(t, definition.Version(2), latest[len(latest)-1].Version)
}

func TestPublish_RefusesWhatItCannotPublish(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	foreign, err := e.definitions.PublishProfile(ctx, otherOrg, "foreign", definition.ExecutionSettings{})
	require.NoError(t, err)

	// Control: a well-formed request naming the organization's profile is published.
	require.Equal(t, http.StatusCreated, e.publishAs(t, "ok", e.publishBody("ok", "fine"), nil).status)

	// A request id in the body other than the one the authority binds.
	assert.Equal(t, http.StatusForbidden, e.publishAs(t, "bound", e.publishBody("other", "x"), nil).status)

	// A body the authority does not bind.
	digest := e.publishAs(t, "d1", e.publishBody("d1", "x"),
		func(b *console.Binding) { b.BodySHA256 = hex.EncodeToString(make([]byte, 32)) })
	assert.Equal(t, http.StatusForbidden, digest.status)
	assert.Equal(t, console.ErrDigestMismatch.Error(), digest.message)

	// A body that is not one publish request.
	unknown := []byte(`{"requestId":"u1","profile":{"name":"small","version":1},"configuration":{},"name":"x"}`)
	assert.Equal(t, http.StatusBadRequest, e.publishAs(t, "u1", unknown, nil).status)

	// A profile only another organization published.
	b := publishBodyOf("f1", versionOf(foreign.Name, int64(foreign.Version)), testConfiguration("", keyRef))
	assert.Equal(t, http.StatusNotFound, e.publishAs(t, "f1", b, nil).status)

	// A create or configure handoff for the publication.
	for _, op := range []string{operationCreate, console.OperationConfigure, console.OperationTemplateRead} {
		refused := e.publishAs(t, "o1", e.publishBody("o1", "x"), func(b *console.Binding) { b.Operation = op })
		assert.Equal(t, http.StatusForbidden, refused.status, op)
	}

	// A request target the authority did not bind.
	target := e.publishAs(t, "t1", e.publishBody("t1", "x"),
		func(b *console.Binding) { b.RequestTarget = "/v1/orgs/" + org + "/templates/other/versions" })
	assert.Equal(t, http.StatusForbidden, target.status)
	assert.Equal(t, "operation authority binds another request target", target.message)

	latest, err := e.definitions.ListTemplates(ctx, org)
	require.NoError(t, err)
	assert.Len(t, latest, 2, "only the control and the env's own template exist")
}

func TestProfiles_ListEveryPublishedVersion(t *testing.T) {
	e := newEnv(t)
	_, err := e.definitions.PublishProfile(context.Background(), org, e.profile.Name, definition.ExecutionSettings{})
	require.NoError(t, err)
	_, err = e.definitions.PublishProfile(context.Background(), org, "large", definition.ExecutionSettings{})
	require.NoError(t, err)

	c := request{http.MethodGet, "/v1/orgs/" + org + "/profiles", nil}
	got := e.send(t, c, e.bound(c, console.OperationProfileRead, orgTarget, nil), nil)
	require.Equal(t, http.StatusOK, got.status, got.message)
	assert.Equal(t, []any{
		answered("large", 1),
		answered(e.profile.Name, 1),
		answered(e.profile.Name, 2),
	}, got.body["profiles"])
}

func TestExecution_ShowsNoEffectiveExecutionUntilARuntimeReportIsAccepted(t *testing.T) {
	e := newEnv(t)
	c := request{http.MethodGet, "/v1/orgs/" + org + "/agents/" + agent + "/execution", nil}
	read := func() map[string]any {
		got := e.send(t, c, e.bound(c, console.OperationExecutionRead, agent, nil), nil)
		require.Equal(t, http.StatusOK, got.status, got.message)
		return got.body
	}

	// Revision 1 asked for, nothing rendered, nothing reported.
	assert.Equal(t, map[string]any{"desired": revisionOf("1", true), "rendered": nil, "effective": nil}, read())

	// A controller reporting it rendered revision 1 is not the running agent reporting it runs.
	_, err := e.definitions.RecordStatus(context.Background(), agent, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"desired": revisionOf("1", false), "rendered": map[string]any{"revision": "1"}, "effective": nil,
	}, read())
}

func TestCORS_AnswersOnlyARegisteredOrigin(t *testing.T) {
	const registered = "https://console.example.test"
	e := newEnv(t, registered)
	c := request{http.MethodGet, "/v1/orgs/" + org + "/templates", nil}
	preflight := request{http.MethodOptions, c.target, nil}
	asks := map[string]string{"Access-Control-Request-Method": "GET",
		"Access-Control-Request-Headers": "authorization"}

	// Control: the registered origin gets exactly the agreed answer, and no credentials.
	asks[originHeader] = registered
	allowed := e.send(t, preflight, "", asks)
	assert.Equal(t, http.StatusNoContent, allowed.status)
	assert.Equal(t, registered, allowed.header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, PUT", allowed.header.Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Authorization, Content-Type, Garam-Contract-Version", allowed.header.Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "600", allowed.header.Get("Access-Control-Max-Age"))
	assert.Empty(t, allowed.header.Values("Access-Control-Allow-Credentials"))
	read := e.send(t, c, e.bound(c, "agent-template:read", orgTarget, nil), map[string]string{originHeader: registered})
	assert.Equal(t, http.StatusOK, read.status)
	assert.Equal(t, registered, read.header.Get("Access-Control-Allow-Origin"))

	// Another origin, one differing only in its port or scheme, gets no CORS header at all.
	for _, other := range []string{"https://evil.example.test", registered + ":8443", "http://console.example.test", "null"} {
		asks[originHeader] = other
		refused := e.send(t, preflight, "", asks)
		assert.Equal(t, http.StatusForbidden, refused.status, other)
		assert.Empty(t, corsHeaders(refused.header), other)
		served := e.send(t, c, e.bound(c, "agent-template:read", orgTarget, nil), map[string]string{originHeader: other})
		assert.Empty(t, corsHeaders(served.header), other)
	}
}

func TestCORS_AnswersNothingWhereNoOriginIsRegistered(t *testing.T) {
	e := newEnv(t)
	got := e.send(t, request{http.MethodOptions, "/v1/orgs/" + org + "/templates", nil}, "",
		map[string]string{originHeader: "https://console.example.test", "Access-Control-Request-Method": "GET"})
	assert.Equal(t, http.StatusForbidden, got.status)
	assert.Empty(t, corsHeaders(got.header))
}

// answered is a named, numbered version as a decoded answer holds one.
func answered(name string, version float64) map[string]any {
	m := versionOf(name, 0)
	m["version"] = version
	return m
}

// revisionOf is the desired part of an execution answer, decoded.
func revisionOf(revision string, pending bool) map[string]any {
	return map[string]any{"revision": revision, "pending": pending}
}

// decoded is v as it reads back after a round trip through JSON.
func decoded(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// corsHeaders is every Access-Control-* header in h.
func corsHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		if len(k) > len("Access-Control-") && k[:len("Access-Control-")] == "Access-Control-" {
			out[k] = v
		}
	}
	return out
}
