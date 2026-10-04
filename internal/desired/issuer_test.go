package desired

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// The issuer's walk through Secrets and the route's whole exchange are tested
// in internal/desired/credential against envtest; these reach what only the
// package can: the refusal counter and the issuer's waits.

// memoryStore is a CredentialStore in memory, holding one managed agent.
type memoryStore struct {
	mu      sync.Mutex
	need    Need
	request *PendingRequest
	placed  bool
}

func (m *memoryStore) Needed(context.Context) ([]Need, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.placed {
		return nil, nil
	}

	return []Need{m.need}, nil
}

func (m *memoryStore) LoadRequest(context.Context, string) (PendingRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return PendingRequest{}, false, nil
	}

	return *m.request, true, nil
}

func (m *memoryStore) SaveRequest(_ context.Context, _ string, request PendingRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.request = &request

	return nil
}

func (m *memoryStore) Place(context.Context, string, []byte, Certificate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placed = true

	return nil
}

// countingRoute answers every certificate request with status and body, a
// refusal where body is empty, and counts them.
type countingRoute struct {
	mu       sync.Mutex
	status   int
	body     string
	requests int
}

func (c *countingRoute) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	w.WriteHeader(c.status)
	if c.body != "" {
		_, _ = w.Write([]byte(c.body))

		return
	}
	_, _ = w.Write([]byte(`{"kind":"not_authorized","message":"refused"}`))
}

func (c *countingRoute) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.requests
}

// startIssuer runs an issuer against a route answering status and body until
// the test ends, passing every millisecond and retrying a transient failure after
// one, and returns the route and the store.
func startIssuer(t *testing.T, status int, body string) (*countingRoute, *memoryStore) {
	t.Helper()

	route := &countingRoute{status: status, body: body}
	store := &memoryStore{need: Need{GRN: agentA, Epoch: "7"}}
	server := httptest.NewTLSServer(route)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	issuer := NewIssuer(NewClient(server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		store)
	issuer.pass, issuer.transientFirst, issuer.transientLast, issuer.refusedWait =
		time.Millisecond, time.Millisecond, time.Millisecond, time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		_ = issuer.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	return route, store
}

func TestIssuerCountsARefusalAndAsksAgainOnlyAfterTheLongWait(t *testing.T) {
	g := NewWithT(t)

	By := "the control: a 503 is asked again on the short wait"
	unavailable, _ := startIssuer(t, http.StatusServiceUnavailable, "")
	g.Eventually(unavailable.count).Should(BeNumerically(">=", 3), By)

	By = "a 403 is counted under the route and not asked again before the long wait"
	before := valueOf(refusalsTotal.WithLabelValues(routeCertificateRequests, "403"))
	forbidden, _ := startIssuer(t, http.StatusForbidden, "")
	g.Eventually(forbidden.count).Should(Equal(1), By)
	g.Eventually(func() float64 { return valueOf(refusalsTotal.WithLabelValues(routeCertificateRequests, "403")) }).
		Should(Equal(before+1), By)
	g.Consistently(forbidden.count, 300*time.Millisecond).Should(Equal(1), By)
}

// placed reports whether the store was given a credential.
func (m *memoryStore) isPlaced() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.placed
}

func TestIssuerPlacesNoAnswerForAnotherEpoch(t *testing.T) {
	g := NewWithT(t)
	answer := func(epoch string) string {
		return `{"agent":"` + agentA + `","epoch":"` + epoch +
			`","certificatePem":"c","issuerPem":"i","serverRootPem":"r","notAfter":"2026-10-06T00:00:00Z"}`
	}

	By := "the control: an answer for the agent and epoch asked is placed"
	_, matching := startIssuer(t, http.StatusCreated, answer("7"))
	g.Eventually(matching.isPlaced).Should(BeTrue(), By)

	By = "an answer naming another epoch is not placed, and not asked again soon"
	route, other := startIssuer(t, http.StatusCreated, answer("6"))
	g.Eventually(route.count).Should(Equal(1), By)
	g.Consistently(route.count, 300*time.Millisecond).Should(Equal(1), By)
	g.Expect(other.isPlaced()).To(BeFalse(), By)
}
