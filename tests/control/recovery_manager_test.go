//go:build e2e

package control_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
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

// managerStore is the manager's recovery store in memory, holding one agent's first credential
// as the issuer placed it, and nothing of any other agent.
type managerStore struct {
	mu                  sync.Mutex
	agent               string
	issuer              []byte
	request             *desired.PendingRequest
	key, certificatePEM []byte
	lineage, refused    string
}

func (m *managerStore) Recovering(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return nil, nil
	}
	return []string{m.agent}, nil
}

func (m *managerStore) LoadRecovery(_ context.Context, agent string) (desired.PendingRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if agent != m.agent || m.request == nil {
		return desired.PendingRequest{}, false, nil
	}
	return *m.request, true, nil
}

func (m *managerStore) SaveRecovery(_ context.Context, agent string, request desired.PendingRequest) error {
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

func (m *managerStore) KeptIssuer(_ context.Context, agent string) ([]byte, bool, error) {
	return m.issuer, agent == m.agent, nil
}

func (m *managerStore) RefuseRecovery(_ context.Context, _, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refused = reason
	return nil
}

func (m *managerStore) PlaceRecovered(_ context.Context, _ string, key, certificatePEM []byte, lineage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.key, m.certificatePEM, m.lineage, m.request = key, certificatePEM, lineage, nil
	return nil
}

// state is what the store holds, read under its lock.
func (m *managerStore) state() (request *desired.PendingRequest, certificatePEM []byte, lineage, refused string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.request, m.certificatePEM, m.lineage, m.refused
}

// TestRecovery_TheManagersHalfAgainstGaram is #300's full path through the built control service
// and a real garam: the console opens a recovery, the manager's recoverer prepares it over a key it
// persists first, an administrator finalizes it at garam, and the recoverer places the recovered
// certificate only once it verifies against the issuer kept from the first certificate. garam
// then fences the first lineage, and the recovered pair activates the next generation.
func TestRecovery_TheManagersHalfAgainstGaram(t *testing.T) {
	grn, epoch := managedAgent(t)
	key, csr := certificateRequestPEM(t)
	status, raw, err := requestCertificate(grn, certificateRequestBody(name(t, "certificate"), epoch, csr))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var first struct{ CertificatePem, IssuerPem string }
	require.NoError(t, json.Unmarshal(raw, &first))
	a := placedAgent{grn: grn, epoch: epoch, token: name(t, "placement-token"),
		pair: tls.Certificate{Certificate: [][]byte{parsePEM(t, first.CertificatePem).Raw}, PrivateKey: key}}
	mustPlace(t, grn, a.placement(t), http.StatusCreated)
	activated(t, a, strings.Repeat("9", 32))

	store := &managerStore{agent: grn, issuer: []byte(first.IssuerPem)}
	client := desired.NewClient(strings.TrimPrefix(attachedURL, "https://"),
		real.feedClient().Transport.(*http.Transport).TLSClientConfig)
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
	status, opened := sendMinted(t, grn, "/recovery", "agent:recover", openID, open)
	require.Equal(t, http.StatusCreated, status, opened)
	answer, err := client.Desired(ctx, "", 0)
	require.NoError(t, err)
	offered := map[string]desired.OpenRecovery{}
	for _, agent := range answer.Agents {
		if agent.Recovery != nil {
			offered[agent.GRN] = *agent.Recovery
		}
	}
	require.Equal(t, desired.OpenRecovery{RequestID: recoveryID, Epoch: epoch}, offered[grn],
		"the manager's client does not read the open recovery off the feed")
	// Earlier tests leave other agents' recoveries open; this manager holds none of their credentials.
	recoverer.Offer(map[string]desired.OpenRecovery{grn: offered[grn]})

	// The recoverer persists its request and prepares the recovery with it.
	require.Eventually(t, func() bool { return readRecovery(t, grn)["stage"] == "prepared" }, time.Minute, time.Second,
		"the recoverer did not prepare the recovery")
	persisted, _, _, _ := store.state()
	require.NotNil(t, persisted)
	prepared := []byte(readRecovery(t, grn)["body"].(string))
	var sent struct {
		RequestID             string `json:"requestId"`
		CertificateRequestPEM string `json:"certificateRequestPem"`
	}
	require.NoError(t, json.Unmarshal(prepared, &sent))
	assert.Equal(t, string(persisted.CSRPEM), sent.CertificateRequestPEM,
		"control was sent another request than the one persisted")
	assert.Equal(t, recoveryID, sent.RequestID)

	// An administrator finalizes it at garam under a handoff over the prepared bytes.
	status, finalized := sendMinted(t, grn, "/recovery/finalize", "agent:recover", recoveryID, prepared)
	require.Equal(t, http.StatusCreated, status, finalized)

	// The recoverer places what garam recovered once it verifies against the kept issuer.
	require.Eventually(t, func() bool { _, placed, _, _ := store.state(); return placed != nil }, time.Minute, time.Second,
		"the recoverer did not place the recovered credential")
	request, certificatePEM, lineage, refused := store.state()
	assert.Empty(t, refused)
	assert.Nil(t, request, "the request outlived the placed credential")
	assert.NotEmpty(t, lineage)
	assert.Equal(t, finalized["certificatePem"], string(certificatePEM))
	recovered, err := tls.X509KeyPair(certificatePEM, persisted.KeyPEM)
	require.NoError(t, err, "the placed certificate is not over the persisted key")

	// garam fenced the first lineage, and the recovered pair activates the next generation.
	old := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), epoch, strings.Repeat("a", 32)))
	assert.Equal(t, http.StatusForbidden, old.status, old.raw)
	next := postAgent(t, a, recovered, "activations", activationOf(name(t, "activation"), epoch, strings.Repeat("b", 32)))
	assert.Equal(t, http.StatusCreated, next.status, next.raw)
}
