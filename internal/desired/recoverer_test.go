package desired

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// recoveryID is the open recovery every test's feed names.
const recoveryID = "rec-1"

// authority is a certificate authority a test signs with.
type authority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

func newAuthority(t *testing.T, name string) authority {
	t.Helper()
	g := NewWithT(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	g.Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	template.Subject.CommonName = name
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	g.Expect(err).NotTo(HaveOccurred())
	certificate, err := x509.ParseCertificate(der)
	g.Expect(err).NotTo(HaveOccurred())

	return authority{certificate: certificate, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der})}
}

// sign issues a certificate naming grn over the key in csrPEM, as garam signs a recovery.
func (a authority) sign(csrPEM []byte, grn string) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	san, err := url.Parse(grn)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), URIs: []*url.URL{san},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, csr.PublicKey, a.key)
	if err != nil {
		return nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), nil
}

// recoveryStore is a RecoveryStore in memory, holding one managed agent's placed credential.
type recoveryStore struct {
	mu      sync.Mutex
	issuer  []byte
	request *PendingRequest
	refused string
	placed  []byte
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

func (m *recoveryStore) LoadRecovery(context.Context, string) (PendingRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		return PendingRequest{}, false, nil
	}

	return *m.request, true, nil
}

func (m *recoveryStore) SaveRecovery(_ context.Context, _ string, request PendingRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.request == nil {
		m.request = &request
	}

	return nil
}

func (m *recoveryStore) KeptIssuer(context.Context, string) ([]byte, bool, error) {
	return m.issuer, true, nil
}

func (m *recoveryStore) RefuseRecovery(_ context.Context, _, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refused = reason

	return nil
}

func (m *recoveryStore) PlaceRecovered(_ context.Context, _ string, _, certificatePEM []byte, lineage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placed, m.lineage, m.request = certificatePEM, lineage, nil

	return nil
}

// snapshot is what the store holds, read under its lock.
func (m *recoveryStore) snapshot() (request *PendingRequest, refused string, placed []byte, lineage string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.request, m.refused, m.placed, m.lineage
}

// recoveryRoute stands in for the control service's recovery-requests route. It records every
// request it is sent and whether the store held it then; it answers 202 until finalized is set,
// and then 200 with a certificate signer issues over the request's key.
type recoveryRoute struct {
	mu        sync.Mutex
	store     *recoveryStore
	signer    authority
	finalized bool
	sent      []wireCertificateRequest
	persisted []bool
}

func (r *recoveryRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var in wireCertificateRequest
	_ = json.NewDecoder(req.Body).Decode(&in)
	held, _, _, _ := r.store.snapshot()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, in)
	r.persisted = append(r.persisted, held != nil && string(held.CSRPEM) == in.CertificateRequestPEM)
	answer := wireRecovered{Agent: agentA, RecoveryRequestID: in.RequestID, Epoch: in.Epoch, Stage: "prepared"}
	status := http.StatusAccepted
	if r.finalized {
		certificate, err := r.signer.sign([]byte(in.CertificateRequestPEM), agentA)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			return
		}
		answer.Stage, answer.Lineage, answer.CertificatePEM = "finalized", "lineage-2", string(certificate)
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(answer)
}

func (r *recoveryRoute) finalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalized = true
}

func (r *recoveryRoute) requests() ([]wireCertificateRequest, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]wireCertificateRequest(nil), r.sent...), append([]bool(nil), r.persisted...)
}

// startRecoverer runs a recoverer over store against route until the returned stop is called,
// offered agentA's open recovery, passing every millisecond.
func startRecoverer(t *testing.T, route *recoveryRoute, store *recoveryStore) (stop func()) {
	t.Helper()
	server := httptest.NewTLSServer(route)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	recoverer := NewRecoverer(NewClient(server.Listener.Addr().String(),
		&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), store)
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
	kept := newAuthority(t, "kept issuer")
	store := &recoveryStore{issuer: kept.pem}
	route := &recoveryRoute{store: store, signer: kept}

	stop := startRecoverer(t, route, store)
	g.Eventually(func() int { sent, _ := route.requests(); return len(sent) }).Should(BeNumerically(">=", 1))
	stop()
	first, persisted := route.requests()
	g.Expect(persisted).To(HaveEach(BeTrue()), "a recovery request was sent before it was persisted")
	g.Expect(first[0].RequestID).To(Equal(recoveryID))
	g.Expect(first[0].Epoch).To(Equal("7"))

	By := "a restart sends the request it persisted, never a new key"
	startRecoverer(t, route, store)
	g.Eventually(func() int { sent, _ := route.requests(); return len(sent) }).Should(BeNumerically(">", len(first)), By)
	after, _ := route.requests()
	for _, sent := range after {
		g.Expect(sent).To(Equal(first[0]), By)
	}
}

func TestRecovererPlacesOnlyACertificateTheKeptIssuerSigned(t *testing.T) {
	g := NewWithT(t)
	kept := newAuthority(t, "kept issuer")

	By := "the control: a certificate the kept issuer signed over the persisted key is placed"
	store := &recoveryStore{issuer: kept.pem}
	route := &recoveryRoute{store: store, signer: kept}
	startRecoverer(t, route, store)
	g.Eventually(func() *PendingRequest { request, _, _, _ := store.snapshot(); return request }).ShouldNot(BeNil(), By)
	route.finalize()
	g.Eventually(func() []byte { _, _, placed, _ := store.snapshot(); return placed }).ShouldNot(BeEmpty(), By)
	request, refused, _, lineage := store.snapshot()
	g.Expect(lineage).To(Equal("lineage-2"), By)
	g.Expect(refused).To(BeEmpty(), By)
	g.Expect(request).To(BeNil(), "the request was kept after its certificate was placed")

	By = "a certificate another authority signed is refused: nothing is placed, and the request is kept"
	other := newAuthority(t, "another issuer")
	elsewhere := &recoveryStore{issuer: kept.pem}
	foreign := &recoveryRoute{store: elsewhere, signer: other, finalized: true}
	startRecoverer(t, foreign, elsewhere)
	g.Eventually(func() string { _, refused, _, _ := elsewhere.snapshot(); return refused }).
		Should(Equal(agentv1alpha1.ReasonRecoveredCertificateUnverified), By)
	held, _, placed, _ := elsewhere.snapshot()
	g.Expect(placed).To(BeEmpty(), By)
	g.Expect(held).NotTo(BeNil(), By)
}

func TestVerifyRecoveredRefusesAnythingButTheKeptIssuersCertificateOverTheKey(t *testing.T) {
	g := NewWithT(t)
	kept, other := newAuthority(t, "kept issuer"), newAuthority(t, "another issuer")
	request, err := newPendingRequest("7")
	g.Expect(err).NotTo(HaveOccurred())
	another, err := newPendingRequest("7")
	g.Expect(err).NotTo(HaveOccurred())
	sign := func(a authority, csr []byte, grn string) []byte {
		certificate, err := a.sign(csr, grn)
		g.Expect(err).NotTo(HaveOccurred())

		return certificate
	}

	// The control: the kept issuer's certificate over the persisted key, naming the agent.
	g.Expect(VerifyRecovered(agentA, request.KeyPEM, sign(kept, request.CSRPEM, agentA), kept.pem)).To(Succeed())

	g.Expect(VerifyRecovered(agentA, request.KeyPEM, sign(other, request.CSRPEM, agentA), kept.pem)).
		To(MatchError(ContainSubstring("does not chain to the kept issuer")))
	g.Expect(VerifyRecovered(agentA, request.KeyPEM, sign(kept, another.CSRPEM, agentA), kept.pem)).
		To(MatchError(ContainSubstring("not signed over the persisted key")))
	g.Expect(VerifyRecovered(agentA, request.KeyPEM, sign(kept, request.CSRPEM, agentB), kept.pem)).
		To(MatchError(ContainSubstring("names")))
	g.Expect(VerifyRecovered(agentA, request.KeyPEM, sign(kept, request.CSRPEM, agentA), nil)).
		To(MatchError(ContainSubstring("no issuer is kept")))
}
