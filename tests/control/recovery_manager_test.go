//go:build e2e

package control_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// managerStore is the manager's recovery store in memory, for one agent and no other.
type managerStore struct {
	mu      sync.Mutex
	agent   string
	request *desired.RecoveryRequest
	lineage string
}

func (m *managerStore) Recovering(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return nil, nil
	}
	return []string{m.agent}, nil
}

func (m *managerStore) LoadRecovery(_ context.Context, agent string) (desired.RecoveryRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if agent != m.agent || m.request == nil {
		return desired.RecoveryRequest{}, false, nil
	}
	return *m.request, true, nil
}

func (m *managerStore) SaveRecovery(_ context.Context, agent string, request desired.RecoveryRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if agent != m.agent {
		return fmt.Errorf("the store holds no credential of %s", agent)
	}
	if m.request == nil {
		m.request = &request
	}
	return nil
}

func (m *managerStore) Recovered(_ context.Context, _, lineage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lineage, m.request = lineage, nil
	return nil
}

// state is what the store holds, read under its lock.
func (m *managerStore) state() (request *desired.RecoveryRequest, lineage string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.request, m.lineage
}

// TestRecovery_TheManagersHalfAgainstGaram is #300's full path through the built control service
// and a real garam, under execution-fence.v2: the console opens a recovery, the manager's recoverer
// prepares it from a request it persists first, an administrator finalizes it at garam, and the
// recoverer records the new lineage. garam then refuses the first lineage's tokens, and a token the
// controller mints in the new one activates the next generation.
func TestRecovery_TheManagersHalfAgainstGaram(t *testing.T) {
	a := placeAgent(t)
	activated(t, a, strings.Repeat("9", 32))

	store := &managerStore{agent: a.grn}
	client := desired.NewClient(strings.TrimPrefix(attachedURL, "https://"), real.feedTransport())
	recoverer := desired.NewRecoverer(client, store)
	// The recoverer's log is the test's, so a failure shows what it met.
	logged := funcr.New(func(prefix, args string) { t.Log(prefix, args) }, funcr.Options{})
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), logged))
	stopped := make(chan struct{})
	go func() {
		_ = recoverer.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	// The console opens the recovery, and the feed names it, as the puller offers it.
	openID, recoveryID := name(t, "open"), name(t, "recovery")
	open := requestBody(t, struct {
		RequestID         string `json:"requestId"`
		RecoveryRequestID string `json:"recoveryRequestId"`
	}{openID, recoveryID})
	status, opened := sendMinted(t, a.grn, "/recovery", "agent:recover", openID, open)
	require.Equal(t, http.StatusCreated, status, opened)
	answer, err := client.Desired(ctx, "", 0)
	require.NoError(t, err)
	offered := map[string]desired.OpenRecovery{}
	for _, agent := range answer.Agents {
		if agent.Recovery != nil {
			offered[agent.GRN] = *agent.Recovery
		}
	}
	require.Equal(t, desired.OpenRecovery{RequestID: recoveryID, Epoch: a.epoch}, offered[a.grn],
		"the manager's client does not read the open recovery off the feed")
	// Earlier tests leave other agents' recoveries open; this manager holds none of their credentials.
	recoverer.Offer(map[string]desired.OpenRecovery{a.grn: offered[a.grn]})

	// The recoverer persists its request and prepares the recovery with it.
	require.Eventually(t, func() bool { return readRecovery(t, a.grn)["stage"] == "prepared" }, time.Minute, time.Second,
		"the recoverer did not prepare the recovery")
	persisted, _ := store.state()
	require.NotNil(t, persisted)
	assert.Equal(t, desired.RecoveryRequest{ID: recoveryID, Epoch: a.epoch}, *persisted)
	prepared := []byte(readRecovery(t, a.grn)["body"].(string))
	assert.JSONEq(t, recoveryRequestBody(recoveryID, a.epoch), string(prepared),
		"control was sent another request than the one persisted")

	// An administrator finalizes it at garam under a handoff over the prepared bytes.
	status, finalized := sendMinted(t, a.grn, "/recovery/finalize", "agent:recover", recoveryID, prepared)
	require.Equal(t, http.StatusCreated, status, finalized)

	// The recoverer records the lineage garam started.
	require.Eventually(t, func() bool { _, lineage := store.state(); return lineage != "" }, time.Minute, time.Second,
		"the recoverer did not record the recovered lineage")
	request, lineage := store.state()
	assert.Nil(t, request, "the request outlived the recorded lineage")
	assert.Equal(t, finalized["lineage"], lineage)

	// garam refuses the first lineage's token, and a token minted in the new one activates the next
	// generation.
	old := postAgent(t, a, a.control, "activations", activationOf(name(t, "activation"), a.epoch, strings.Repeat("a", 32)))
	assert.Equal(t, http.StatusForbidden, old.status, old.raw)
	next := postAgent(t, a, controlToken(t, a.grn), "activations",
		activationOf(name(t, "activation"), a.epoch, strings.Repeat("b", 32)))
	assert.Equal(t, http.StatusCreated, next.status, next.raw)
}
