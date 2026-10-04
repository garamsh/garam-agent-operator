package introspector_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/introspector"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const bindingAnswer = `{"operationRef":"ref-1","grantId":"grant-1","orgGrn":"grn:root:default:org:acme",
"actorGrn":"grn:acme:default:user:7c1d","audienceGrn":"grn:root:default:operator:control",
"operation":"agent:configure","targetGrn":"grn:acme:default:agent:0a1b",
"assignment":{"operatorGrn":"grn:acme:default:operator:k8s","epoch":"7"},
"requestId":"c1","bodySha256":"ab","expiresAt":"2026-10-05T12:05:00Z"}`

// garam is a stand-in machine listener answering each introspection with the next status.
type garam struct {
	statuses []int
	calls    atomic.Int32
	contract string
}

func (g *garam) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(g.calls.Add(1)) - 1
	g.contract = r.Header.Get("Garam-Contract-Version")
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["authority"] != "the-authority" ||
		r.URL.Path != "/operation-authorities/introspection" || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	status := g.statuses[min(n, len(g.statuses)-1)]
	w.Header().Set("Garam-Contract-Version", "operation-authority.v1")
	w.WriteHeader(status)
	if status == http.StatusOK {
		_, _ = w.Write([]byte(bindingAnswer))
	} else {
		_, _ = w.Write([]byte(`{"message":"internal error"}`))
	}
}

func introspect(t *testing.T, g http.Handler) (console.Binding, error) {
	t.Helper()
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return introspector.NewGaram(machine).Introspect(context.Background(), "the-authority")
}

func TestGaram_ReadsTheBinding(t *testing.T) {
	g := &garam{statuses: []int{200}}
	b, err := introspect(t, g)
	require.NoError(t, err)
	assert.Equal(t, "operation-authority.v1", g.contract)
	assert.Equal(t, console.Binding{
		OperationRef: "ref-1", GrantID: "grant-1", Org: "grn:root:default:org:acme",
		Actor: "grn:acme:default:user:7c1d", Audience: "grn:root:default:operator:control",
		Operation: "agent:configure", Target: "grn:acme:default:agent:0a1b",
		Assignment: &console.Assignment{Operator: "grn:acme:default:operator:k8s", Epoch: "7"},
		RequestID:  "c1", BodySHA256: "ab", ExpiresAt: time.Date(2026, 10, 5, 12, 5, 0, 0, time.UTC),
	}, b)
}

func TestGaram_RefusalsAreNotRetried(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, console.ErrAuthorityUnknown},
		{http.StatusForbidden, console.ErrAuthorityForbidden},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			g := &garam{statuses: []int{tt.status, 200}}
			_, err := introspect(t, g)
			require.ErrorIs(t, err, tt.want)
			assert.Equal(t, int32(1), g.calls.Load())
		})
	}
}

func TestGaram_UndecidedIsUndecided(t *testing.T) {
	g := &garam{statuses: []int{http.StatusServiceUnavailable}}
	_, err := introspect(t, g)
	require.ErrorIs(t, err, console.ErrAuthorityUndecided)

	// Control: an undecided answer followed by a decided one is the decided one.
	_, err = introspect(t, &garam{statuses: []int{http.StatusServiceUnavailable, 200}})
	require.NoError(t, err)
}

func TestGaram_ContractErrorIsNotARefusal(t *testing.T) {
	g := &garam{statuses: []int{http.StatusBadRequest}}
	_, err := introspect(t, g)
	require.Error(t, err)
	assert.NotErrorIs(t, err, console.ErrAuthorityUnknown)
	assert.NotErrorIs(t, err, console.ErrAuthorityUndecided)
	assert.Equal(t, int32(1), g.calls.Load())
}
