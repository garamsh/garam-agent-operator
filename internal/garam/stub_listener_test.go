package garam_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// stubListener stands in for garam's machine listener: it terminates TLS with a
// certificate of its own and requests no client certificate, answers a token
// at /oauth/token, refuses a call carrying no token it answered on every other
// route, and records what each request it served asked for and presented.
//
// A call refused there was not served, and reaches neither the handler nor the
// record.
type stubListener struct {
	server *httptest.Server

	// trustFile holds the certificate this listener presents, which is what a
	// client verifies it against.
	trustFile string

	mu     sync.Mutex
	seen   []stubRequest
	issued map[string]bool
}

// stubRequest is one request the stub listener served.
type stubRequest struct {
	method string
	path   string

	// body is what the request carried, which is empty on the routes that send
	// none.
	body string

	// token is the bearer token the request presented.
	token string
}

// tokenPath is garam's exchangeClientAssertion route.
const tokenPath = "/oauth/token"

// newStubListener starts a listener serving handler and stops it with the test.
func newStubListener(t *testing.T, handler http.HandlerFunc) *stubListener {
	t.Helper()

	dir := t.TempDir()
	certificatePEM, keyPEM := newCertificate(t, "garam-machine")
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatalf("load the stub listener's certificate: %v", err)
	}

	stub := &stubListener{trustFile: writeFile(t, dir, "trust.pem", certificatePEM), issued: map[string]bool{}}
	stub.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			stub.answerToken(w)
			return
		}
		if !stub.authenticated(r) {
			answerJSON(w, http.StatusUnauthorized, `{"kind":"unauthenticated","message":"no machine identity"}`)
			return
		}
		stub.record(r)
		handler(w, r)
	}))
	stub.server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}
	stub.server.StartTLS()
	t.Cleanup(stub.server.Close)

	return stub
}

// answerToken answers a fresh token, as garam answers an exchange it admits.
// What an assertion carries is internal/machinetoken's to test; here it is
// enough that the operator exchanged before it called.
func (s *stubListener) answerToken(w http.ResponseWriter) {
	s.mu.Lock()
	token := fmt.Sprintf("garam_ot_%064d", len(s.issued)+1)
	s.issued[token] = true
	s.mu.Unlock()
	w.Header().Set("Garam-Contract-Version", "machine-token.v2")
	answerJSON(w, http.StatusOK, `{"access_token":"`+token+`","token_type":"Bearer","expires_in":3600}`)
}

// authenticated reports whether a request presented a token this listener
// answered.
func (s *stubListener) authenticated(r *http.Request) bool {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	return found && s.issued[token]
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
	request.token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, request)
}

// trustedBy is the transport verifying stub and nothing else, and presenting a
// token exchanged at it under a key issuance written to a fresh directory.
func trustedBy(t *testing.T, stub *stubListener) http.RoundTripper {
	t.Helper()

	tlsConfig, err := garam.ServerTLS(stub.trustFile)
	if err != nil {
		t.Fatalf("configure TLS against the stub listener: %v", err)
	}
	base := &http.Transport{TLSClientConfig: tlsConfig}
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: "https://" + stub.address() + tokenPath,
		Files:    writeKeyIssuance(t, t.TempDir()),
		HTTP:     &http.Client{Transport: base},
	})
	return &machinetoken.Transport{Source: source, Base: base}
}

// writeKeyIssuance writes the three files a key issuance places, and answers
// them.
func writeKeyIssuance(t *testing.T, dir string) machinetoken.Files {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the operator's key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal the operator's key: %v", err)
	}
	return machinetoken.Files{
		PrivateKey: writeFile(t, dir, "private-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		ClientID:   writeFile(t, dir, "client-id", []byte("grn:acme:default:operator:controller")),
		TokenURL:   writeFile(t, dir, "token-url", []byte("https://machine.garam.example/oauth/token")),
	}
}

// answerNoDefinitions is what a listener with nothing to say answers a read.
func answerNoDefinitions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`[]`))
}

// TestStubListenerRefusesACallCarryingNoToken is what makes the rest of this
// package evidence that this operator presents one. A listener that served a
// caller presenting nothing would answer an operator that stopped presenting
// its identity exactly as it answers one that presents it.
func TestStubListenerRefusesACallCarryingNoToken(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerNoDefinitions)

	tlsConfig, err := garam.ServerTLS(stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	presentingNothing := &http.Transport{TLSClientConfig: tlsConfig}
	_, err = garam.NewClient(stub.address(), presentingNothing).ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("401 unauthenticated")))
	g.Expect(stub.requests()).To(BeEmpty())

	// The control, one input away from the call above: the same route under a
	// token is answered, so what refused that call is the missing token and not
	// the route.
	_, err = garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].token).To(HavePrefix("garam_ot_"))
}

// newCertificate mints a self-signed certificate under commonName, valid for the
// hour around now, the shape garam's listener presents today, and returns it and
// its key PEM-encoded.
func newCertificate(t *testing.T, commonName string) (certificatePEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate a key for %s: %v", commonName, err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
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

func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
