package cutover_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/cutover"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const (
	agent = "grn:acme:default:agent:1e9ac1"
	// keyImportID is the import's member in every stage's body.
	keyImportID = "importId"
)

// seen is what the stand-in garam last received.
type seen struct {
	method, target, contract, authorization string
	body                                    string
}

// stand is a stand-in garam under prefix, answering status and answer under agent-cutover.v1.
func stand(t *testing.T, prefix string, status int, answer string) (*cutover.Garam, *seen) {
	t.Helper()
	got := &seen{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*got = seen{r.Method, r.URL.RequestURI(), r.Header.Get("Garam-Contract-Version"), r.Header.Get("Authorization"), string(raw)}
		w.Header().Set("Garam-Contract-Version", garammachine.AgentCutover)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL+prefix, server.Client())
	machine.Backoff = time.Millisecond
	return cutover.NewGaram(machine), got
}

func TestGaram_ReadsTheSourceUnderTheReadsReference(t *testing.T) {
	g, got := stand(t, "", http.StatusOK, `{"agent":"`+agent+`","assignee":"op","epoch":"3","mode":"legacy","class":"eligible",
		"source":{"operator":"op","values":{"k":"v"},"definedBy":"u","createdAt":"2026-10-05T00:00:00Z"},"sourceDigest":"`+
		"aa"+`","attempt":null}`)
	source, err := g.Read(context.Background(), agent, "read-ref")
	require.NoError(t, err)
	assert.Equal(t, console.CutoverSource{Agent: agent, Assignee: "op", Epoch: "3", Mode: "legacy", Class: "eligible",
		HasSource: true, Operator: "op", Values: map[string]string{"k": "v"}, SourceDigest: "aa"}, source)
	assert.Equal(t, seen{http.MethodGet, "/agents/" + agent + "/cutover", garammachine.AgentCutover, "Garam-Operation read-ref", ""}, *got)
	assert.Equal(t, got.target, g.Target(agent, console.StageRead), "the target the reference binds is not the one sent")
}

func TestGaram_SendsEachStageToItsRoute(t *testing.T) {
	attempt := `{"importId":"i1","stage":"frozen","frozenDigest":"aa","createdAt":"2026-10-05T00:00:00Z","switchedAt":null,"endedAt":null}`
	calls := []struct {
		stage console.Stage
		call  func(g *cutover.Garam) (console.CutoverAttempt, error)
		body  map[string]string
	}{
		{console.StageFreeze, func(g *cutover.Garam) (console.CutoverAttempt, error) {
			return g.Freeze(context.Background(), agent, "ref", "i1", "aa", "3")
		}, map[string]string{keyImportID: "i1", "sourceDigest": "aa", "epoch": "3"}},
		{console.StageSwitch, func(g *cutover.Garam) (console.CutoverAttempt, error) {
			return g.Switch(context.Background(), agent, "ref", "i1")
		}, map[string]string{keyImportID: "i1"}},
		{console.StageRollback, func(g *cutover.Garam) (console.CutoverAttempt, error) {
			return g.RollBack(context.Background(), agent, "ref", "i1")
		}, map[string]string{keyImportID: "i1"}},
	}
	for _, c := range calls {
		t.Run(string(c.stage), func(t *testing.T) {
			g, got := stand(t, "/machine", http.StatusOK, attempt)
			a, err := c.call(g)
			require.NoError(t, err)
			assert.Equal(t, console.CutoverAttempt{ImportID: "i1", Stage: "frozen", FrozenDigest: "aa"}, a)
			assert.Equal(t, http.MethodPost, got.method)
			assert.Equal(t, "/machine/agents/"+agent+"/cutover/"+string(c.stage), got.target)
			assert.Equal(t, got.target, g.Target(agent, c.stage), "a listener's path prefix is not in the target")
			var body map[string]string
			require.NoError(t, json.Unmarshal([]byte(got.body), &body))
			assert.Equal(t, c.body, body)
		})
	}
}

func TestGaram_RefusalsCarryGaramsReason(t *testing.T) {
	g, _ := stand(t, "", http.StatusConflict, `{"kind":"failed_precondition","message":"m","reason":"attempt_open"}`)
	_, err := g.Switch(context.Background(), agent, "ref", "i1")
	var refused *console.CutoverRefusal
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, console.CutoverRefusal{
		Status: http.StatusConflict, Kind: "attempt_open", Reason: "attempt_open", Message: "m",
	}, *refused)

	// Control: a refusal naming no reason carries garam's kind.
	g, _ = stand(t, "", http.StatusForbidden, `{"kind":"permission_denied","message":"no"}`)
	_, err = g.Switch(context.Background(), agent, "ref", "i1")
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "permission_denied", refused.Kind)
	assert.Empty(t, refused.Reason, "garam's errorx kind was carried as the contract's reason")

	g, _ = stand(t, "", http.StatusServiceUnavailable, `{}`)
	_, err = g.Switch(context.Background(), agent, "ref", "i1")
	require.ErrorIs(t, err, console.ErrCutoverUndecided)
}
