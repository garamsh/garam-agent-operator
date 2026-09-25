package garam_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// enrollmentPath is the route an operator obtains its first certificate on
// (garam@b16a896:api/machine.yaml:147).
const enrollmentPath = "/enrollment"

// enrolledOperator is the identity garam's answer names. A token names the
// operator it was minted for and the request names none, so this is the answer's
// to say and never the request's.
const enrolledOperator = "grn:garam:default:operator:gagent"

// answerEnrollment answers an enrollment the way garam does: it reads the public
// key and the signature out of the request and nothing else, signs a certificate
// naming the operator the token named, answers the garam server root it is given
// to answer, and answers no private key because it generated none
// (garam@b16a896:api/machine.yaml:811-849).
func answerEnrollment(t *testing.T, serverRootPEM []byte) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		presented := struct {
			Token                 string `json:"token"`
			CertificateRequestPEM string `json:"certificateRequestPem"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&presented); err != nil {
			t.Errorf("decode the enrollment presented: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"grn":%q,"certificatePem":%q,"issuerPem":%q,"serverRootPem":%q,"notAfter":%q}`,
			enrolledOperator, signRequest(t, []byte(presented.CertificateRequestPEM)),
			"an-issuer", serverRootPEM, time.Now().Add(time.Hour).Format(time.RFC3339))
	}
}

// signRequest mints a certificate over the public key a request carries, which
// is what garam does with one: it holds no key of the subject and reads nothing
// else out of the request.
func signRequest(t *testing.T, requestPEM []byte) []byte {
	t.Helper()

	block, _ := pem.Decode(requestPEM)
	if block == nil {
		t.Fatalf("the enrollment presented no PEM block")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("read the certificate signing request presented: %v", err)
	}
	if err := request.CheckSignature(); err != nil {
		t.Fatalf("verify the certificate signing request presented: %v", err)
	}

	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate an issuing key: %v", err)
	}
	issuer := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "issuer"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, &issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("mint an issuing certificate: %v", err)
	}
	signer, err := x509.ParseCertificate(issuerDER)
	if err != nil {
		t.Fatalf("read the issuing certificate back: %v", err)
	}

	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: enrolledOperator},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, signer, request.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("sign the key the request carried: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newEnrollmentStub starts a listener answering enrollments with the certificate
// it presents, which is the ordinary case: the root garam answers is the one the
// caller already verified it by. The handler reads it back through the listener
// because the trust file is written as the listener starts.
func newEnrollmentStub(t *testing.T) *stubListener {
	t.Helper()

	var stub *stubListener
	stub = newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		answerEnrollment(t, readFile(t, stub.trustFile))(w, r)
	})
	return stub
}

// readFile answers what a file holds, failing the test where it cannot be read.
func readFile(t *testing.T, name string) []byte {
	t.Helper()

	content, err := os.ReadFile(name)
	if err != nil {
		t.Errorf("read %s: %v", name, err)
	}
	return content
}

// answerStatus answers every request with garam's error body under status.
func answerStatus(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"kind":"unauthenticated","message":"refused"}`))
	}
}

// newEnrollmentClient starts a listener serving handler at the enrollment route
// and returns a client reaching it the way an enrolling operator does: verifying
// the listener against the root the deployment supplied, presenting nothing.
func newEnrollmentClient(t *testing.T) (*garam.Client, *stubListener) {
	t.Helper()

	return clientFor(t, newEnrollmentStub(t))
}

// newRefusingClient is the same client against a listener that refuses with
// status.
func newRefusingClient(t *testing.T, status int) (*garam.Client, *stubListener) {
	t.Helper()

	return clientFor(t, newStubListener(t, answerStatus(status)))
}

// clientFor answers a client reaching stub the way an enrolling operator does:
// verifying the listener against the root the deployment supplied, presenting
// nothing.
func clientFor(t *testing.T, stub *stubListener) (*garam.Client, *stubListener) {
	t.Helper()

	tlsConfig, err := garam.EnrollmentTLS(stub.trustFile)
	if err != nil {
		t.Fatalf("configure the connection to the stub listener: %v", err)
	}
	return garam.NewClient(stub.address(), tlsConfig), stub
}

// tamperWithSignature returns the request with the last byte of its DER flipped,
// which lands inside the signature. What it produces still parses as PKCS#10 —
// the test asserts that — so what refuses it is the signature check and not the
// encoding.
func tamperWithSignature(t *testing.T, request garam.CertificateRequest) garam.CertificateRequest {
	t.Helper()

	block, _ := pem.Decode(request.RequestPEM)
	if block == nil {
		t.Fatalf("the request built holds no PEM block")
	}
	tampered := append([]byte(nil), block.Bytes...)
	tampered[len(tampered)-1] ^= 0xff
	request.RequestPEM = pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: tampered})
	return request
}

func TestNewCertificateRequestBuildsAPKCS10RequestOverAP256Key(t *testing.T) {
	g := NewWithT(t)

	request, err := garam.NewCertificateRequest()

	g.Expect(err).NotTo(HaveOccurred())
	block, _ := pem.Decode(request.RequestPEM)
	g.Expect(block).NotTo(BeNil())
	g.Expect(block.Type).To(Equal("CERTIFICATE REQUEST"))

	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(parsed.CheckSignature()).To(Succeed())

	public, ok := parsed.PublicKey.(*ecdsa.PublicKey)
	g.Expect(ok).To(BeTrue())
	g.Expect(public.Curve).To(Equal(elliptic.P256()))

	// The key answers the request: a certificate signed over one and stored
	// beside the other authenticates nothing.
	keyBlock, _ := pem.Decode(request.KeyPEM)
	g.Expect(keyBlock).NotTo(BeNil())
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(key.(*ecdsa.PrivateKey).PublicKey.Equal(public)).To(BeTrue())
}

// TestNewCertificateRequestNamesNoSubject holds this operator to what garam
// reads. A subject here is discarded rather than refused, so nothing fails when
// one is added — and what a certificate names is then written in two places,
// one of which garam ignores.
func TestNewCertificateRequestNamesNoSubject(t *testing.T) {
	g := NewWithT(t)

	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	block, _ := pem.Decode(request.RequestPEM)
	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(parsed.Subject.String()).To(BeEmpty())
	g.Expect(parsed.DNSNames).To(BeEmpty())
	g.Expect(parsed.URIs).To(BeEmpty())
}

func TestEnrollReturnsTheCertificateGaramSigned(t *testing.T) {
	g := NewWithT(t)
	client, stub := newEnrollmentClient(t)
	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	enrolled, err := client.Enroll(context.Background(), "a-token", request)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(enrolled.Operator).To(Equal(garam.GRN(enrolledOperator)))
	g.Expect(string(enrolled.CertificatePEM)).To(ContainSubstring("BEGIN CERTIFICATE"))
	g.Expect(enrolled.NotAfter).To(BeTemporally(">", time.Now()))

	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].path).To(Equal(enrollmentPath))
	g.Expect(stub.requests()[0].body).To(ContainSubstring(`"token":"a-token"`))
	g.Expect(stub.requests()[0].body).To(ContainSubstring("BEGIN CERTIFICATE REQUEST"))
	// The one route on this listener a caller reaches with no certificate. An
	// operator that had one to present would not be enrolling.
	g.Expect(stub.requests()[0].client).To(BeEmpty())
}

// TestEnrollSendsNoRequestWhoseSignatureDoesNotVerify is the check that stands
// between a malformed request and a spent token: garam spends the token on the
// attempt, so a request it would refuse costs a registration.
//
// The control is the same request untampered, which is sent and answered. What
// separates the two is the signature alone: the tampered request is asserted to
// parse as PKCS#10, so nothing but the signature check can be refusing it.
func TestEnrollSendsNoRequestWhoseSignatureDoesNotVerify(t *testing.T) {
	g := NewWithT(t)
	client, stub := newEnrollmentClient(t)
	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	tampered := tamperWithSignature(t, request)
	block, _ := pem.Decode(tampered.RequestPEM)
	parsed, err := x509.ParseCertificateRequest(block.Bytes)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(parsed.CheckSignature()).NotTo(Succeed())

	_, err = client.Enroll(context.Background(), "a-token", tampered)

	g.Expect(err).To(HaveOccurred())
	g.Expect(stub.requests()).To(BeEmpty())

	// The control. The same call with the signature intact is sent, so what
	// refused the one above is the signature and not the call.
	_, err = client.Enroll(context.Background(), "a-token", request)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()).To(HaveLen(1))
}

// TestEnrollReportsARefusedTokenAsOneAnswer holds the client to what garam
// answers: spent, expired and never-minted are one refusal and a caller that
// told them apart would be inferring which.
func TestEnrollReportsARefusedTokenAsOneAnswer(t *testing.T) {
	g := NewWithT(t)
	client, _ := newRefusingClient(t, http.StatusUnauthorized)
	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	_, err = client.Enroll(context.Background(), "a-token", request)
	g.Expect(err).To(MatchError(garam.ErrTokenNotUsable))

	// The control: the other status this route answers is a request garam
	// refused, which is not a token to register again for.
	malformed, _ := newRefusingClient(t, http.StatusBadRequest)
	_, err = malformed.Enroll(context.Background(), "a-token", request)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).NotTo(MatchError(garam.ErrTokenNotUsable))
}

func TestEnrollRefusesAnAnswerCarryingNoCertificate(t *testing.T) {
	g := NewWithT(t)
	client, _ := clientFor(t, newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"grn":"` + enrolledOperator + `"}`))
	}))
	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	_, err = client.Enroll(context.Background(), "a-token", request)

	g.Expect(err).To(HaveOccurred())
}

// TestEnrollmentTLSVerifiesGaramAgainstTheRootItWasGiven is what keeps the token
// from being handed to whoever answered. An enrollment carries the token before
// anything comes back, so a connection that is not verified has already given it
// away — and the root the answer carries cannot be what verified the answer.
//
// The control is the same call verified against the listener's own root, which
// is answered.
func TestEnrollmentTLSVerifiesGaramAgainstTheRootItWasGiven(t *testing.T) {
	g := NewWithT(t)
	stub := newEnrollmentStub(t)
	request, err := garam.NewCertificateRequest()
	g.Expect(err).NotTo(HaveOccurred())

	elsewhere, _ := newCertificate(t, "another-garam")
	tlsConfig, err := garam.EnrollmentTLS(writeFile(t, t.TempDir(), "trust.pem", elsewhere))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = garam.NewClient(stub.address(), tlsConfig).Enroll(context.Background(), "a-token", request)

	g.Expect(err).To(HaveOccurred())
	g.Expect(stub.requests()).To(BeEmpty())

	// The control: the same listener, verified against the root it presents.
	trusted, err := garam.EnrollmentTLS(stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = garam.NewClient(stub.address(), trusted).Enroll(context.Background(), "a-token", request)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()).To(HaveLen(1))
}

// startEnroller runs an Enroller until it returns or the test ends, and answers
// a channel closed when Start returned.
func startEnroller(t *testing.T, ctx context.Context, enroller *garam.Enroller) chan struct{} {
	t.Helper()

	ctx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = enroller.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return stopped
}

// newEnroller returns an Enroller reaching stub with the token and credential
// files a test placed, writing what it obtains through store.
func newEnroller(t *testing.T, stub *stubListener, store garam.CredentialStore, token, dir string) *garam.Enroller {
	t.Helper()

	tlsConfig, err := garam.EnrollmentTLS(stub.trustFile)
	if err != nil {
		t.Fatalf("configure the connection to the stub listener: %v", err)
	}
	tokenFile := dir + "/enrollment-token"
	if token != "" {
		tokenFile = writeFile(t, dir, "enrollment-token", []byte(token))
	}
	return garam.NewEnroller(garam.NewClient(stub.address(), tlsConfig), store, tokenFile,
		dir+"/certificate.pem", dir+"/key.pem", stub.trustFile)
}

// TestEnrollerStoresTheCertificateBesideTheKeyItGenerated is the whole of what
// enrollment is for. The certificate garam signed and the key this operator kept
// are a pair or they are nothing: garam generated no key and can answer no
// second copy of one.
func TestEnrollerStoresTheCertificateBesideTheKeyItGenerated(t *testing.T) {
	g := NewWithT(t)
	stub := newEnrollmentStub(t)
	store := newRecordingStore(nil)

	stopped := startEnroller(t, context.Background(), newEnroller(t, stub, store, "a-token", t.TempDir()))

	g.Eventually(stopped, time.Second*10).Should(BeClosed())
	g.Expect(store.written).To(HaveLen(1))
	stored := <-store.written
	_, err := tls.X509KeyPair(stored.CertificatePEM, stored.KeyPEM)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].path).To(Equal(enrollmentPath))
}

// TestEnrollerSpendsATokenOnAnExpiredCertificateAndNothingOnALiveOne is what
// makes an operator whose credential lapsed recoverable. A certificate that
// expired authenticates nothing, so the renewal that would replace it is
// refused for holding it, and an operator that read it as an identity would
// wait on a route it had closed against itself.
//
// The control is the live certificate, which is the credential the renewer
// renews and no token replaces: a restart must not spend one to replace what
// still works. The two differ in notAfter alone — same helper, same loop, same
// listener — so what tells them apart is the validity read off the pair and not
// whether a pair could be read.
func TestEnrollerSpendsATokenOnAnExpiredCertificateAndNothingOnALiveOne(t *testing.T) {
	g := NewWithT(t)
	stub := newEnrollmentStub(t)
	store := newRecordingStore(nil)
	dir := t.TempDir()
	writeIdentityValidUntil(t, dir, "operator", time.Now().Add(-time.Hour))

	stopped := startEnroller(t, context.Background(), newEnroller(t, stub, store, "a-token", dir))

	g.Eventually(stopped, time.Second*10).Should(BeClosed())
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].path).To(Equal(enrollmentPath))
	g.Expect(store.written).To(HaveLen(1))

	// The control: the same certificate an hour from expiring rather than an
	// hour past it.
	live := newEnrollmentStub(t)
	liveStore := newRecordingStore(nil)
	liveDir := t.TempDir()
	writeIdentityValidUntil(t, liveDir, "operator", time.Now().Add(time.Hour))

	liveStopped := startEnroller(t, context.Background(), newEnroller(t, live, liveStore, "a-token", liveDir))

	g.Eventually(liveStopped, time.Second*10).Should(BeClosed())
	g.Expect(live.requests()).To(BeEmpty())
	g.Expect(liveStore.written).To(BeEmpty())
}

// TestEnrollerWaitsWhereThereIsNoTokenToSpend is what makes a deployment that
// configured enrollment and has no token behave as one that did not. The flag is
// set wherever this operator runs, so this is the state most of them are in.
func TestEnrollerWaitsWhereThereIsNoTokenToSpend(t *testing.T) {
	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "no file at all", token: ""},
		{name: "a file holding nothing", token: "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			stub := newEnrollmentStub(t)
			store := newRecordingStore(nil)

			stopped := startEnroller(t, context.Background(), newEnroller(t, stub, store, test.token, t.TempDir()))

			g.Consistently(stopped, time.Millisecond*200).ShouldNot(BeClosed())
			g.Expect(stub.requests()).To(BeEmpty())
			g.Expect(store.written).To(BeEmpty())
		})
	}
}

// TestEnrollerPresentsARefusedTokenOnce is the property a retry would break. A
// token is spent by the call that presents it, so a second call presents one
// that is gone and reports a state that has already moved.
//
// The refused token is the only one in the file and is read again before this
// asserts, so one request is what a token buys however often it is read. A loop
// that had stopped would read the same way here, and what separates the two is
// [TestEnrollerPresentsAReplacementTokenAndNeverTheOneItSpent].
func TestEnrollerPresentsARefusedTokenOnce(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerStatus(http.StatusUnauthorized))
	store := newRecordingStore(nil)

	startEnroller(t, context.Background(), newEnroller(t, stub, store, "a-token", t.TempDir()))

	g.Eventually(stub.requests, time.Second*10).Should(HaveLen(1))
	// Longer than the interval the token file is read on, so the refused token
	// is read again before this is asserted.
	g.Consistently(stub.requests, time.Second*12).Should(HaveLen(1))
	g.Expect(store.written).To(BeEmpty())
}

// newEnrollmentStubRefusing starts a listener answering enrollments as
// [newEnrollmentStub] does, except for the one token it refuses: that one is
// answered the way garam answers a token it will not take.
func newEnrollmentStubRefusing(t *testing.T, refused string) *stubListener {
	t.Helper()

	var stub *stubListener
	stub = newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the enrollment presented: %v", err)
			return
		}
		// Put back what was read, because the handler behind this reads the
		// same request.
		r.Body = io.NopCloser(bytes.NewReader(body))

		presented := struct {
			Token string `json:"token"`
		}{}
		if err := json.Unmarshal(body, &presented); err != nil {
			t.Errorf("decode the enrollment presented: %v", err)
			return
		}
		if presented.Token == refused {
			answerStatus(http.StatusUnauthorized)(w, r)
			return
		}
		answerEnrollment(t, readFile(t, stub.trustFile))(w, r)
	})
	return stub
}

// TestEnrollerPresentsAReplacementTokenAndNeverTheOneItSpent is what makes a
// refused token something a person replaces rather than something a restart
// recovers from. A token is spent by the call that presents it, so this
// operator presents one once and goes on reading the file for another.
//
// The refusal is the first token: it stays in the file across a look that reads
// it again and is presented no second time. The control is the second, which
// differs from it only in never having been presented and reaches this operator
// through the same file, the same loop and the same listener — so what holds
// the first back is the record of having spent it, and not a loop that stopped.
func TestEnrollerPresentsAReplacementTokenAndNeverTheOneItSpent(t *testing.T) {
	g := NewWithT(t)
	stub := newEnrollmentStubRefusing(t, "a-refused-token")
	store := newRecordingStore(nil)
	dir := t.TempDir()

	stopped := startEnroller(t, context.Background(), newEnroller(t, stub, store, "a-refused-token", dir))

	g.Eventually(stub.requests, time.Second*10).Should(HaveLen(1))
	// Longer than the interval the token file is read on, so the refused token
	// is read again while it is still the only token there.
	g.Consistently(stub.requests, time.Second*12).Should(HaveLen(1))
	g.Expect(stopped).NotTo(BeClosed())

	// The control: a token this operator has not presented, reaching it through
	// the same file, the same loop and the same listener.
	writeFile(t, dir, "enrollment-token", []byte("a-replacement-token"))

	g.Eventually(stopped, time.Second*15).Should(BeClosed())
	g.Expect(stub.requests()).To(HaveLen(2))
	g.Expect(stub.requests()[1].body).To(ContainSubstring(`"token":"a-replacement-token"`))
	g.Expect(store.written).To(HaveLen(1))
}

// TestEnrollerAsksForNothingElseWhereTheStoreRefusesTheAnswer is the loss no
// token but another one recovers. garam signed a key it never held and answers
// no second copy, so a certificate the store would not take exists nowhere and
// the token that bought it is spent: a second call with it would present a token
// that is already gone. What recovers it is another token, so this waits for one
// rather than ending.
func TestEnrollerAsksForNothingElseWhereTheStoreRefusesTheAnswer(t *testing.T) {
	g := NewWithT(t)
	stub := newEnrollmentStub(t)
	store := newRecordingStore(errors.New("the API server refused the patch"))

	stopped := startEnroller(t, context.Background(), newEnroller(t, stub, store, "a-token", t.TempDir()))

	g.Eventually(stub.requests, time.Second*10).Should(HaveLen(1))
	// Longer than the interval the token file is read on, so the spent token is
	// read again before this is asserted.
	g.Consistently(stub.requests, time.Second*12).Should(HaveLen(1))
	g.Expect(stopped).NotTo(BeClosed())
	g.Expect(store.written).To(HaveLen(1))
}

// recordedLog collects what an Enroller logged, so that a line it emits and
// nothing else can be read back. It is a [testr.TestingT], which is what the
// package's other tests build their logger from.
type recordedLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordedLog) Helper() {}

func (r *recordedLog) Log(args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprint(args...))
}

// logged answers every line holding substring.
func (r *recordedLog) logged(substring string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(r.lines), func(line string) bool { return !strings.Contains(line, substring) })
}

// fingerprintOf is the SHA-256 of the certificate in a PEM, over the DER, which
// is what a reader gets from `openssl x509 -fingerprint -sha256`.
func fingerprintOf(t *testing.T, certificatePEM []byte) string {
	t.Helper()

	block, _ := pem.Decode(certificatePEM)
	if block == nil {
		t.Fatalf("the certificate holds no PEM block")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// TestEnrollerNamesAServerRootTheAnswerAndTheTrustFileDoNotShare is the only
// notice a deployment gets that garam's root is moving ahead of the file that
// verifies garam, which nothing here rewrites. Without it the first symptom is
// every handshake failing at once.
//
// The control is the same enrollment answering the root the call was verified
// by, which says nothing. Both store the credential: this reports and refuses
// nothing, so an operator whose garam has rotated still enrolls.
func TestEnrollerNamesAServerRootTheAnswerAndTheTrustFileDoNotShare(t *testing.T) {
	g := NewWithT(t)
	elsewhere, _ := newCertificate(t, "another-garam")
	stub := newStubListener(t, answerEnrollment(t, elsewhere))
	store := newRecordingStore(nil)

	recorded := &recordedLog{}
	ctx := logf.IntoContext(context.Background(), testr.NewWithInterface(recorded, testr.Options{Verbosity: 1}))
	stopped := startEnroller(t, ctx, newEnroller(t, stub, store, "a-token", t.TempDir()))

	g.Eventually(stopped, time.Second*10).Should(BeClosed())
	named := recorded.logged("not one this operator verifies garam by")
	g.Expect(named).To(HaveLen(1))
	g.Expect(named[0]).To(ContainSubstring(fingerprintOf(t, elsewhere)))
	g.Expect(named[0]).To(ContainSubstring(fingerprintOf(t, readFile(t, stub.trustFile))))
	g.Expect(store.written).To(HaveLen(1))

	// The control: the same answer carrying the root that verified the call.
	shared := newEnrollmentStub(t)
	sharedStore := newRecordingStore(nil)
	sharedLog := &recordedLog{}
	sharedCtx := logf.IntoContext(context.Background(), testr.NewWithInterface(sharedLog, testr.Options{Verbosity: 1}))
	sharedStopped := startEnroller(t, sharedCtx, newEnroller(t, shared, sharedStore, "a-token", t.TempDir()))

	g.Eventually(sharedStopped, time.Second*10).Should(BeClosed())
	g.Expect(sharedLog.logged("not one this operator verifies garam by")).To(BeEmpty())
	g.Expect(sharedStore.written).To(HaveLen(1))
}
