package garam_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// stubListener stands in for garam's machine listener: it terminates TLS with a
// certificate of its own and asks for a client certificate without requiring
// one, as garam's does (garam@b16a896:internal/machine/listener.go:29), refuses
// a call that presented none on every route but the enrollment, and records what
// each request it served asked for and authenticated as.
//
// Requesting rather than requiring is what leaves room for the one route a
// caller reaches before it has a certificate at all. It leaves the handshake
// admitting a caller that presents nothing on every other route too, so what
// those routes require is refused above the handshake instead. A call refused
// there was not served, and reaches neither the handler nor the record.
type stubListener struct {
	server *httptest.Server

	// trustFile holds the certificate this listener presents, which is what a
	// client verifies it against.
	trustFile string

	mu   sync.Mutex
	seen []stubRequest
}

// stubRequest is one request the stub listener served.
type stubRequest struct {
	method string
	path   string

	// body is what the request carried, which is empty on the routes that send
	// none.
	body string

	// client is the common name of the certificate the request presented.
	client string
}

// newStubListener starts a listener serving handler and stops it with the test.
func newStubListener(t *testing.T, handler http.HandlerFunc) *stubListener {
	t.Helper()

	dir := t.TempDir()
	certificatePEM, keyPEM := newCertificate(t, "garam-machine")
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatalf("load the stub listener's certificate: %v", err)
	}

	stub := &stubListener{trustFile: writeFile(t, dir, "trust.pem", certificatePEM)}
	stub.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			answerJSON(w, http.StatusUnauthorized, `{"kind":"unauthenticated","message":"no machine identity"}`)
			return
		}
		stub.record(r)
		handler(w, r)
	}))
	stub.server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequestClientCert,
	}
	stub.server.StartTLS()
	t.Cleanup(stub.server.Close)

	return stub
}

// authenticated reports whether a request carried what its route requires: a
// certificate everywhere but the enrollment, which a caller reaches before it
// has one.
//
// A call carrying none is refused above the handshake, because the handshake
// admits it — which is what puts the enrollment on this listener at all. The
// answer is the 401 garam answered an operator whose identity it would not
// take, per ADR 0022.
func authenticated(r *http.Request) bool {
	return r.URL.Path == enrollmentPath || len(r.TLS.PeerCertificates) > 0
}

// address is the host and port the stub listener answers on.
func (s *stubListener) address() string {
	return s.server.Listener.Addr().String()
}

// requests is what the stub listener has served so far.
func (s *stubListener) requests() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest(nil), s.seen...)
}

// record notes one request. It reads the body and puts it back, because the
// handler behind this reads the same request.
func (s *stubListener) record(r *http.Request) {
	request := stubRequest{method: r.Method, path: r.URL.Path}
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			request.body = string(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		request.client = r.TLS.PeerCertificates[0].Subject.CommonName
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, request)
}

// TestStubListenerRefusesACallCarryingNoCertificateOnEveryRouteButTheEnrollment
// is what makes the rest of this package evidence that this operator presents
// one. A listener that served a caller presenting nothing would answer an
// operator that stopped presenting its identity exactly as it answers one that
// presents it.
func TestStubListenerRefusesACallCarryingNoCertificateOnEveryRouteButTheEnrollment(t *testing.T) {
	g := NewWithT(t)
	routes := http.NewServeMux()
	routes.HandleFunc("/definitions", answerNoDefinitions)
	routes.HandleFunc(enrollmentPath, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusCreated, `{"certificatePem":"a certificate"}`)
	})
	stub := newStubListener(t, routes.ServeHTTP)

	presentingNothing, err := garam.EnrollmentTLS(stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = garam.NewClient(stub.address(), presentingNothing).ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("401 unauthenticated")))

	// The controls, each one input away from the call above. The same route with
	// a certificate presented is answered, so what refused that call is the
	// certificate and not the route; and the same caller presenting nothing is
	// answered on the enrollment, so it is the route requiring one and not the
	// listener refusing whatever it is sent.
	_, err = garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())
	_, err = garam.NewClient(stub.address(), presentingNothing).Enroll(context.Background(), "a-token", request)
	g.Expect(err).NotTo(HaveOccurred())
}

// newCertificate mints a self-signed certificate under commonName, the shape
// garam's listener presents today, and returns it and its key PEM-encoded.
func newCertificate(t *testing.T, commonName string) (certificatePEM, keyPEM []byte) {
	t.Helper()

	return newCertificateValidUntil(t, commonName, time.Now().Add(time.Hour))
}

// newCertificateValidUntil is [newCertificate] with the notAfter chosen, which
// is what separates a certificate this operator can authenticate with from one
// it cannot. It is valid for the two hours ending there, so a notAfter already
// past mints a certificate that expired rather than one no clock ever accepted.
func newCertificateValidUntil(t *testing.T, commonName string, notAfter time.Time) (certificatePEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate a key for %s: %v", commonName, err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notAfter.Add(-2 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("mint a certificate for %s: %v", commonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal the key for %s: %v", commonName, err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// writeIdentity writes a certificate under commonName and its key into dir, and
// returns their paths. Called twice with one dir it replaces what is there,
// which is how a renewed certificate reaches a running operator.
func writeIdentity(t *testing.T, dir, commonName string) (certificateFile, keyFile string) {
	t.Helper()

	return writeIdentityValidUntil(t, dir, commonName, time.Now().Add(time.Hour))
}

// writeIdentityValidUntil is [writeIdentity] with the notAfter chosen.
func writeIdentityValidUntil(t *testing.T, dir, commonName string, notAfter time.Time) (certificateFile, keyFile string) {
	t.Helper()

	certificatePEM, keyPEM := newCertificateValidUntil(t, commonName, notAfter)
	return writeFile(t, dir, "certificate.pem", certificatePEM), writeFile(t, dir, "key.pem", keyPEM)
}

func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
