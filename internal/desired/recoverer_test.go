package desired

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// recoveryID is the open recovery every test's feed names.
const recoveryID = "rec-1"

// recoveryStore is a RecoveryStore in memory, holding one managed agent's persisted request and
// the lineage recorded on its credential.
type recoveryStore struct {
	mu      sync.Mutex
	request *RecoveryRequest
	lineage string
}

func (m *recoveryStore) Recovering(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return nil, nil
	}

	return []string{agentA}, nil
}

func (m *recoveryStore) LoadRecovery(context.Context, string) (RecoveryRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return RecoveryRequest{}, false, nil
	}

	return *m.request, true, nil
}

func (m *recoveryStore) SaveRecovery(_ context.Context, _ string, request RecoveryRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		m.request = &request
	}

	return nil
}

func (m *recoveryStore) Recovered(_ context.Context, _, lineage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lineage, m.request = lineage, nil

	return nil
}

// snapshot is what the store holds, read under its lock.
func (m *recoveryStore) snapshot() (request *RecoveryRequest, lineage string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.request, m.lineage
}

// recoveryRoute stands in for the control service's recovery-requests route. It records every
// body it is sent and whether the store held that request then; it answers 202 until finalized
// is set, and then 200 with the new lineage.
type recoveryRoute struct {
	mu        sync.Mutex
	store     *recoveryStore
	finalized bool
	sent      []string
	persisted []bool
}

func (r *recoveryRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var in wireRecoveryRequest
	_ = json.Unmarshal(body, &in)
	held, _ := r.store.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, string(body))
	r.persisted = append(r.persisted, held != nil && held.ID == in.RequestID && held.Epoch == in.Epoch)
	answer := wireRecovered{Agent: agentA, RecoveryRequestID: in.RequestID, Epoch: in.Epoch, Stage: "prepared"}
	status := http.StatusAccepted
	if r.finalized {
		answer.Stage, answer.Lineage, status = "finalized", "lineage-2", http.StatusOK
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(answer)
}

func (r *recoveryRoute) finalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalized = true
}

func (r *recoveryRoute) requests() ([]string, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.sent...), append([]bool(nil), r.persisted...)
}

// startRecoverer runs a recoverer over store against route until the returned stop is called,
// offered agentA's open recovery, passing every millisecond.
func startRecoverer(t *testing.T, route *recoveryRoute, store *recoveryStore) (stop func()) {
	t.Helper()
	server := httptest.NewTLSServer(route)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	recoverer := NewRecoverer(NewClient(server.Listener.Addr().String(), &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}), store)
	recoverer.pass, recoverer.transientFirst, recoverer.transientLast = time.Millisecond, time.Millisecond, time.Millisecond
	recoverer.refusedWait, recoverer.preparedWait = time.Hour, time.Millisecond
	recoverer.Offer(map[string]OpenRecovery{agentA: {RequestID: recoveryID, Epoch: "7"}})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		_ = recoverer.Start(ctx)
		close(stopped)
	}()
	once := sync.Once{}
	stop = func() {
		once.Do(func() {
			cancel()
			<-stopped
		})
	}
	t.Cleanup(stop)

	return stop
}

func TestRecovererPersistsTheRequestBeforeSendingItAndSendsTheSameOneAfterARestart(t *testing.T) {
	g := NewWithT(t)
	store := &recoveryStore{}
	route := &recoveryRoute{store: store}

	stop := startRecoverer(t, route, store)
	g.Eventually(func() int { sent, _ := route.requests(); return len(sent) }).Should(BeNumerically(">=", 1))
	stop()
	first, persisted := route.requests()
	g.Expect(persisted).To(HaveEach(BeTrue()), "a recovery request was sent before it was persisted")
	// The request garam's recovery under execution-fence.v2 is sent, and no certificate request.
	g.Expect(first[0]).To(MatchJSON(`{"requestId": "` + recoveryID + `", "epoch": "7"}`))

	By := "a restart sends the request it persisted"
	startRecoverer(t, route, store)
	g.Eventually(func() int { sent, _ := route.requests(); return len(sent) }).Should(BeNumerically(">", len(first)), By)
	after, _ := route.requests()
	for _, sent := range after {
		g.Expect(sent).To(Equal(first[0]), By)
	}
}

func TestRecovererRecordsTheNewLineageOnceTheRecoveryIsFinalized(t *testing.T) {
	g := NewWithT(t)
	store := &recoveryStore{}
	route := &recoveryRoute{store: store}
	startRecoverer(t, route, store)

	// Prepared, nothing is recorded: an administrator's finalize is what comes next.
	g.Eventually(func() *RecoveryRequest { request, _ := store.snapshot(); return request }).ShouldNot(BeNil())
	g.Consistently(func() string { _, lineage := store.snapshot(); return lineage }, 100*time.Millisecond).
		Should(BeEmpty(), "a lineage was recorded before the recovery was finalized")

	route.finalize()
	g.Eventually(func() string { _, lineage := store.snapshot(); return lineage }).Should(Equal("lineage-2"))
	// The feed still names the recovery until its next answer; it is not begun again.
	g.Consistently(func() *RecoveryRequest { request, _ := store.snapshot(); return request }, 200*time.Millisecond).
		Should(BeNil(), "a request outlived its recorded lineage, or a new one was begun under the recorded recovery")
}
