package garam_test

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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

const (
	notAfterMetric = "garam_operator_certificate_not_after_timestamp_seconds"
	refusalsMetric = "garam_operator_refusals_total"

	// metricInterval is short enough that a test sees several passes.
	metricInterval = 20 * time.Millisecond
)

// sampled answers the value the manager's registry exports for the series of
// name carrying labels, and zero where it exports none.
func sampled(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather the manager's registry: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, series := range family.GetMetric() {
			matched := 0
			for _, pair := range series.GetLabel() {
				if labels[pair.GetName()] == pair.GetValue() {
					matched++
				}
			}
			if matched != len(labels) || len(series.GetLabel()) != len(labels) {
				continue
			}
			if series.GetCounter() != nil {
				return series.GetCounter().GetValue()
			}
			return series.GetGauge().GetValue()
		}
	}
	return 0
}

// refusals answers how many refusals the registry has counted against runnable
// under kind.
func refusals(t *testing.T, runnable, kind string) float64 {
	t.Helper()

	return sampled(t, refusalsMetric, map[string]string{"runnable": runnable, "kind": kind})
}

// run starts runnable and answers what stops it, which the test's end calls
// too where the test did not.
func run(t *testing.T, runnable manager.Runnable) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = runnable.Start(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-stopped
		})
	}
	t.Cleanup(stop)
	return stop
}

// TestCertificateNotAfterIsTheCertificateLastReadToAuthenticateWith is what an
// alert on this operator's credential running out reads. It is set where the
// certificate is read at startup, and again at a handshake after the file under
// it was replaced, which is how a renewal reaches a running operator.
func TestCertificateNotAfterIsTheCertificateLastReadToAuthenticateWith(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerNoDefinitions)
	dir := t.TempDir()
	first := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	certificateFile, keyFile := writeIdentityValidUntil(t, dir, "operator", first)

	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(sampled(t, notAfterMetric, nil)).To(Equal(float64(first.Unix())))

	renewed := first.Add(5 * time.Hour)
	writeIdentityValidUntil(t, dir, "operator", renewed)
	_, err = garam.NewClient(stub.address(), tlsConfig).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(sampled(t, notAfterMetric, nil)).To(Equal(float64(renewed.Unix())))
}

// TestRenewerCountsARefusalAndNotARenewalGaramAdmitsNoSoonerYet keeps the
// renewer's series an alert can read. garam answers 409 to "too early" and to
// "superseded" alike, and "too early" is the answer for two thirds of every
// certificate's life, so counting it would make the renewer's series rise
// every interval of a healthy operator.
//
// A request is served only after the pass before it returned, so the stub
// having served three means two passes counted what they were going to.
func TestRenewerCountsARefusalAndNotARenewalGaramAdmitsNoSoonerYet(t *testing.T) {
	g := NewWithT(t)
	before := refusals(t, "renewer", "409")

	superseded := newStubListener(t, answerConflict("already_exists"))
	stop := run(t, garam.NewRenewer(garam.NewClient(superseded.address(), trustedBy(t, superseded)),
		newRecordingStore(nil), metricInterval))
	g.Eventually(func() int { return len(superseded.requests()) }, 5*time.Second).Should(BeNumerically(">=", 3))
	g.Expect(refusals(t, "renewer", "409") - before).To(BeNumerically(">=", 2))
	stop()

	// The control: the same status under the kind that means "too early".
	before = refusals(t, "renewer", "409")
	tooEarly := newStubListener(t, answerConflict("failed_precondition"))
	run(t, garam.NewRenewer(garam.NewClient(tooEarly.address(), trustedBy(t, tooEarly)),
		newRecordingStore(nil), metricInterval))
	g.Eventually(func() int { return len(tooEarly.requests()) }, 5*time.Second).Should(BeNumerically(">=", 3))
	g.Expect(refusals(t, "renewer", "409")).To(Equal(before))
}

// newHandshakeRefusingListener starts a listener that requires a client
// certificate signed by an authority nothing in these tests signs with, which
// refuses every operator at the handshake the way garam refused one whose
// authority it no longer held. It answers where it listens and the file it is
// verified against.
func newHandshakeRefusingListener(t *testing.T) (address, trustFile string) {
	t.Helper()

	certificatePEM, keyPEM := newCertificate(t, "garam-machine")
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		t.Fatalf("load the listener's certificate: %v", err)
	}
	unrelatedPEM, _ := newCertificate(t, "an-authority-nothing-signs-with")
	authorities := x509.NewCertPool()
	authorities.AppendCertsFromPEM(unrelatedPEM)

	server := httptest.NewUnstartedServer(http.HandlerFunc(answerNoDefinitions))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    authorities,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	return server.Listener.Addr().String(), writeFile(t, t.TempDir(), "trust.pem", certificatePEM)
}

// TestPollerCountsARefusalAtTheHandshake is the failure of 2026-09-03, when
// garam refused this operator's certificate at the handshake for want of the
// authority that signed it and every check this operator could make locally
// still passed.
//
// The control is a handshake failed on this side: the same poller reaching a
// listener it does not verify. That is not garam refusing anything, so what the
// first half counts is garam's alert and not any handshake that failed.
func TestPollerCountsARefusalAtTheHandshake(t *testing.T) {
	g := NewWithT(t)
	before := refusals(t, "poller", "handshake")

	address, trustFile := newHandshakeRefusingListener(t)
	certificateFile, keyFile := writeIdentity(t, t.TempDir(), "operator")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	stop := run(t, garam.NewPoller(garam.NewClient(address, tlsConfig), newRecordingConstructor(), metricInterval))

	g.Eventually(func() float64 { return refusals(t, "poller", "handshake") - before }, 5*time.Second).
		Should(BeNumerically(">=", 2))
	stop()

	// The control: the same identity against a listener it does not verify,
	// which fails the handshake on this side on every pass.
	before = refusals(t, "poller", "handshake")
	unverified := newStubListener(t, answerNoDefinitions)
	notTrusting := garam.NewClient(unverified.address(), tlsConfig)
	_, err = notTrusting.ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("certificate signed by unknown authority")))

	run(t, garam.NewPoller(notTrusting, newRecordingConstructor(), metricInterval))

	g.Consistently(func() float64 { return refusals(t, "poller", "handshake") }, 10*metricInterval).
		Should(Equal(before))
}

// TestReporterCountsARefusalByTheStatusGaramAnswered carries a refusal the
// client maps to one of its own errors, which is where the status would be lost
// if the mapping did not keep it.
func TestReporterCountsARefusalByTheStatusGaramAnswered(t *testing.T) {
	g := NewWithT(t)
	before := refusals(t, "reporter", "403")
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusForbidden, `{"kind": "permission_denied", "message": "not assigned"}`)
	})
	observer := &recordingObserver{}
	observer.hold(garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady})

	run(t, garam.NewReporter(garam.NewClient(stub.address(), trustedBy(t, stub)), observer, metricInterval))

	g.Eventually(func() int { return len(stub.requests()) }, 5*time.Second).Should(BeNumerically(">=", 3))
	g.Expect(refusals(t, "reporter", "403") - before).To(BeNumerically(">=", 2))
}

// TestEnrollerCountsARefusedToken is the refusal an operator whose credential
// lapsed meets next, where the token it was given is not one garam will take.
func TestEnrollerCountsARefusedToken(t *testing.T) {
	g := NewWithT(t)
	before := refusals(t, "enroller", "401")
	stub := newStubListener(t, answerStatus(http.StatusUnauthorized))

	run(t, newEnroller(t, stub, newRecordingStore(nil), "a-token", t.TempDir()))

	g.Eventually(func() float64 { return refusals(t, "enroller", "401") - before }, 5*time.Second).
		Should(Equal(float64(1)))
}
