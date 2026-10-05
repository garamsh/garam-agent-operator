package desired

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// The store reading Pods and the whole exchange against a route double are
// tested in internal/desired/placement against envtest; these reach what only
// the package can: the waits, the refusal counter, and what the registrar
// remembers.

// fixedPlacements is a PlacementStore holding the placements a test sets.
type fixedPlacements struct {
	mu      sync.Mutex
	current []CurrentPlacement
}

func (f *fixedPlacements) Current(context.Context) ([]CurrentPlacement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]CurrentPlacement(nil), f.current...), nil
}

func (f *fixedPlacements) set(current ...CurrentPlacement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = current
}

// refusingRoute answers every placement with status and kind, and records the
// Pod UIDs it was asked about.
type refusingRoute struct {
	mu     sync.Mutex
	status int
	kind   string
	pods   []string
}

func (r *refusingRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body struct {
		PodUID string `json:"podUid"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pods = append(r.pods, body.PodUID)
	w.WriteHeader(r.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"kind": r.kind, "message": "refused"})
}

func (r *refusingRoute) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.pods...)
}

// startRegistrar runs a registrar against a route answering status and kind,
// passing every millisecond, with every wait a millisecond, until the test ends.
func startRegistrar(t *testing.T, status int, kind string, store PlacementStore) *refusingRoute {
	t.Helper()

	route := &refusingRoute{status: status, kind: kind}
	server := httptest.NewTLSServer(route)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	registrar := NewRegistrar(NewClient(server.Listener.Addr().String(),
		&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), store, func() (string, error) { return "leaf-a", nil })
	registrar.pass, registrar.transientFirst, registrar.transientLast, registrar.refusedWait =
		time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		_ = registrar.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	return route
}

// firstPod is the Pod each test's placement starts on.
const firstPod = "pod-1"

func placementOn(pod string) CurrentPlacement {
	return CurrentPlacement{GRN: agentA, Placement: Placement{Epoch: "7", PodUID: pod, PVCUID: "pvc", TokenSHA256: "token"}}
}

func TestRegistrarNeverResendsASupersededPlacementAndSendsTheNextOne(t *testing.T) {
	g := NewWithT(t)

	By := "the control: a previous_mismatch is a refusal that can clear, asked again after its wait"
	mismatched := &fixedPlacements{}
	mismatched.set(placementOn(firstPod))
	retried := startRegistrar(t, http.StatusConflict, "previous_mismatch", mismatched)
	g.Eventually(func() int { return len(retried.asked()) }).Should(BeNumerically(">=", 3), By)

	for _, kind := range []string{kindPlacementSuperseded, kindEpochSuperseded} {
		By = "a 409 " + kind + " is asked once and never again, however short the wait"
		store := &fixedPlacements{}
		store.set(placementOn(firstPod))
		route := startRegistrar(t, http.StatusConflict, kind, store)
		g.Eventually(route.asked).Should(Equal([]string{firstPod}), By)
		g.Consistently(route.asked, 200*time.Millisecond).Should(Equal([]string{firstPod}), By)

		By = "a new Pod is a new placement, which is asked about once more"
		store.set(placementOn("pod-2"))
		g.Eventually(route.asked).Should(Equal([]string{firstPod, "pod-2"}), By)
		g.Consistently(route.asked, 200*time.Millisecond).Should(Equal([]string{firstPod, "pod-2"}), By)
	}
}

func TestRegistrarCountsARefusalUnderItsRoute(t *testing.T) {
	g := NewWithT(t)
	before := valueOf(refusalsTotal.WithLabelValues(routePlacements, "403"))

	store := &fixedPlacements{}
	store.set(placementOn(firstPod))
	route := startRegistrar(t, http.StatusForbidden, "not_authorized", store)
	g.Eventually(func() int { return len(route.asked()) }).Should(BeNumerically(">=", 1))
	g.Eventually(func() float64 { return valueOf(refusalsTotal.WithLabelValues(routePlacements, "403")) }).
		Should(BeNumerically(">", before))
}

func TestRegistrarForgetsAnAgentWithNoCurrentPlacement(t *testing.T) {
	g := NewWithT(t)

	store := &fixedPlacements{}
	store.set(placementOn(firstPod))
	registrar := NewRegistrar(nil, store, func() (string, error) { return "leaf-a", nil })
	registrar.registrations[agentA] = &registration{placement: placementOn(firstPod).Placement, superseded: true}

	// The control: an agent still listed keeps what is remembered of it.
	registrar.registerAll(context.Background())
	g.Expect(registrar.registrations).To(HaveKey(agentA))

	store.set()
	registrar.registerAll(context.Background())
	g.Expect(registrar.registrations).To(BeEmpty())
}
