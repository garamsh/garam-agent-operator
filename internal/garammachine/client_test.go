package garammachine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// contract is the contract version garam answers under.
const contract = "operation-authority.v1"

// garam answers each call with the next status, under contract.
type garam struct {
	statuses []int
	contract string
	calls    atomic.Int32
	sent     atomic.Value
}

func (g *garam) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := int(g.calls.Add(1)) - 1
	g.sent.Store(r.Header.Get("Garam-Contract-Version"))
	w.Header().Set("Garam-Contract-Version", g.contract)
	w.WriteHeader(g.statuses[min(n, len(g.statuses)-1)])
	_, _ = w.Write([]byte(`{}`))
}

func post(t *testing.T, g *garam) (garammachine.Answer, error) {
	t.Helper()
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	c := garammachine.New(server.URL, server.Client())
	c.Backoff = time.Millisecond
	return c.Post(context.Background(), garammachine.OperationAuthority, "/route", map[string]string{})
}

func TestPost_SendsAndRequiresTheContract(t *testing.T) {
	g := &garam{statuses: []int{200}, contract: contract}
	answer, err := post(t, g)
	require.NoError(t, err)
	assert.Equal(t, 200, answer.Status)
	assert.Equal(t, contract, g.sent.Load())

	for _, answered := range []string{"operation-authority.v2", ""} {
		other := &garam{statuses: []int{200}, contract: answered}
		_, err = post(t, other)
		var foreign *garammachine.ContractError
		require.ErrorAs(t, err, &foreign, "answered under %q", answered)
		assert.Equal(t, garammachine.ContractError{Path: "/route", Status: 200, Got: answered, Want: contract}, *foreign)
		assert.NotErrorIs(t, err, garammachine.ErrUndecided)
	}
}

func TestPost_RetriesUndecidedAnswersWithinTheBound(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			// Control: an undecided answer followed by a decided one is the decided one.
			recovered := &garam{statuses: []int{status, 200}, contract: contract}
			_, err := post(t, recovered)
			require.NoError(t, err)
			assert.Equal(t, int32(2), recovered.calls.Load())

			persistent := &garam{statuses: []int{status}, contract: contract}
			_, err = post(t, persistent)
			require.ErrorIs(t, err, garammachine.ErrUndecided)
			assert.Equal(t, int32(3), persistent.calls.Load())
		})
	}
}

func TestPost_ADecidedAnswerIsNotRetried(t *testing.T) {
	g := &garam{statuses: []int{http.StatusForbidden, 200}, contract: contract}
	answer, err := post(t, g)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, answer.Status)
	assert.Equal(t, int32(1), g.calls.Load())
}
