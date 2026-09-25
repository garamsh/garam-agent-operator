package garam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// renewalPath is the route an operator replaces its own certificate on
// (garam@1150b88:api/machine.yaml:63).
const renewalPath = "/identity/certificate"

// answerRenewal answers a renewal with a freshly minted certificate under
// commonName, in the shape garam's MachineCertificate has
// (garam@1150b88:api/machine/server.gen.go:73-88).
func answerRenewal(t *testing.T, commonName string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, _ *http.Request) {
		certificatePEM, keyPEM := newCertificate(t, commonName)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"grn":            "grn:garam:default:operator:gagent",
			"certificatePem": string(certificatePEM),
			"privateKeyPem":  string(keyPEM),
			"issuerPem":      "issuer",
			"serverRootPem":  "server-root",
			"notAfter":       time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}
}

// answerConflict answers every request with garam's error body under kind. The
// status is 409 whichever kind that is, which is the whole point: garam maps
// already_exists and failed_precondition to one status
// (garam@1150b88:internal/wire/error.go:16).
func answerConflict(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"kind":"` + kind + `","message":"refused"}`))
	}
}

// newRenewalClient starts a listener serving handler at the renewal route and
// returns a client holding an identity in a fresh directory.
func newRenewalClient(t *testing.T, handler http.HandlerFunc) *garam.Client {
	t.Helper()

	stub := newStubListener(t, handler)
	certificateFile, keyFile := writeIdentity(t, t.TempDir(), "operator")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	if err != nil {
		t.Fatalf("configure the connection to the stub listener: %v", err)
	}
	return garam.NewClient(stub.address(), tlsConfig)
}

func TestRenewIdentityReturnsTheCredentialGaramIssued(t *testing.T) {
	g := NewWithT(t)
	client := newRenewalClient(t, answerRenewal(t, "operator-renewed"))

	credential, err := client.RenewIdentity(context.Background())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(credential.CertificatePEM)).To(ContainSubstring("BEGIN CERTIFICATE"))
	g.Expect(string(credential.KeyPEM)).To(ContainSubstring("BEGIN PRIVATE KEY"))
}

// TestRenewIdentityTellsARefusalApartByKindAndNotByStatus is what separates
// "ask again later" from "mint one out of band". garam answers 409 to both
// (garam@1150b88:internal/wire/error.go:16), so a client reading the status
// would map them to one answer and act wrongly on one of the two.
func TestRenewIdentityTellsARefusalApartByKindAndNotByStatus(t *testing.T) {
	g := NewWithT(t)

	tooEarly := newRenewalClient(t, answerConflict("failed_precondition"))
	_, err := tooEarly.RenewIdentity(context.Background())
	g.Expect(err).To(MatchError(garam.ErrRenewalTooEarly))
	g.Expect(err).NotTo(MatchError(garam.ErrCredentialSuperseded))

	superseded := newRenewalClient(t, answerConflict("already_exists"))
	_, err = superseded.RenewIdentity(context.Background())
	g.Expect(err).To(MatchError(garam.ErrCredentialSuperseded))
	g.Expect(err).NotTo(MatchError(garam.ErrRenewalTooEarly))

	// The control: the same status under a kind neither names is neither of
	// them, so what the two assertions above read is the kind and not the 409
	// they share.
	unknown := newRenewalClient(t, answerConflict("aborted"))
	_, err = unknown.RenewIdentity(context.Background())
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).NotTo(MatchError(garam.ErrRenewalTooEarly))
	g.Expect(err).NotTo(MatchError(garam.ErrCredentialSuperseded))
}

func TestRenewIdentityRefusesAnAnswerCarryingNoKey(t *testing.T) {
	g := NewWithT(t)
	client := newRenewalClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"grn":"grn:garam:default:operator:gagent","certificatePem":"a"}`))
	})

	_, err := client.RenewIdentity(context.Background())

	g.Expect(err).To(HaveOccurred())
}

// recordingStore stands in for the Secret the credential lives in.
type recordingStore struct {
	written chan garam.Credential
	err     error
}

func newRecordingStore(err error) *recordingStore {
	return &recordingStore{written: make(chan garam.Credential, 1), err: err}
}

func (s *recordingStore) ReplaceCredential(_ context.Context, credential garam.Credential) error {
	s.written <- credential
	return s.err
}

// runOnce runs one pass of a Renewer and stops it. The interval is longer than
// the test, so what is observed is the pass Start makes before the first tick.
func runOnce(t *testing.T, renewer *garam.Renewer, until func() bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = renewer.Start(ctx)
	}()
	NewWithT(t).Eventually(until, time.Second*10, time.Millisecond*10).Should(BeTrue())
	cancel()
	<-stopped
}

func TestRenewerStoresTheCredentialGaramIssued(t *testing.T) {
	g := NewWithT(t)
	client := newRenewalClient(t, answerRenewal(t, "operator-renewed"))
	store := newRecordingStore(nil)

	renewer := garam.NewRenewer(client, store, time.Hour)
	runOnce(t, renewer, func() bool { return len(store.written) == 1 })

	g.Expect(string((<-store.written).CertificatePEM)).To(ContainSubstring("BEGIN CERTIFICATE"))
}

// TestRenewerStoresNothingWhenGaramRefusesTheRenewalAsTooEarly is the case that
// runs for two thirds of every certificate's life. Storing anything here would
// write over the credential that is still current.
func TestRenewerStoresNothingWhenGaramRefusesTheRenewalAsTooEarly(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerConflict("failed_precondition"))
	certificateFile, keyFile := writeIdentity(t, t.TempDir(), "operator")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	store := newRecordingStore(nil)

	renewer := garam.NewRenewer(garam.NewClient(stub.address(), tlsConfig), store, time.Hour)
	runOnce(t, renewer, func() bool { return len(stub.requests()) == 1 })

	g.Expect(stub.requests()[0].path).To(Equal(renewalPath))
	g.Expect(store.written).To(BeEmpty())
}

// fileStore writes a credential to the files the handshake reads, which is what
// the Secret and the kubelet do between them on a cluster.
type fileStore struct {
	certificateFile string
	keyFile         string
	written         chan struct{}
}

func (s *fileStore) ReplaceCredential(_ context.Context, credential garam.Credential) error {
	if err := os.WriteFile(s.certificateFile, credential.CertificatePEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(s.keyFile, credential.KeyPEM, 0o600); err != nil {
		return err
	}
	s.written <- struct{}{}
	return nil
}

// discardingStore is the same store with the storing taken out: it reports
// success and keeps nothing, which is a renewal lost.
type discardingStore struct{ written chan struct{} }

func (s *discardingStore) ReplaceCredential(context.Context, garam.Credential) error {
	s.written <- struct{}{}
	return nil
}

// TestRenewerReplacesTheCredentialTheNextHandshakePresents is the whole of what
// this mechanism is for, and its control is the same run with the store taken
// out. A renewal garam issued and nothing kept leaves the operator presenting
// the certificate it replaced — which garam will not renew a second time.
func TestRenewerReplacesTheCredentialTheNextHandshakePresents(t *testing.T) {
	g := NewWithT(t)

	routes := http.NewServeMux()
	routes.HandleFunc(renewalPath, answerRenewal(t, "operator-renewed"))
	routes.HandleFunc("/definitions", answerNoDefinitions)
	stub := newStubListener(t, routes.ServeHTTP)

	dir := t.TempDir()
	certificateFile, keyFile := writeIdentity(t, dir, "operator-bootstrap")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	client := garam.NewClient(stub.address(), tlsConfig)

	kept := &fileStore{certificateFile: certificateFile, keyFile: keyFile, written: make(chan struct{}, 1)}
	runOnce(t, garam.NewRenewer(client, kept, time.Hour), func() bool { return len(kept.written) == 1 })

	_, err = client.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()[len(stub.requests())-1].client).To(Equal("operator-renewed"))

	// The control. Everything is the same but for what the renewal is written
	// through, and the handshake that follows presents the replaced
	// certificate — the loss this mechanism exists to prevent.
	lostCertificate, lostKey := writeIdentity(t, t.TempDir(), "operator-bootstrap")
	lostConfig, err := garam.MutualTLS(lostCertificate, lostKey, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	lostClient := garam.NewClient(stub.address(), lostConfig)

	discarded := &discardingStore{written: make(chan struct{}, 1)}
	runOnce(t, garam.NewRenewer(lostClient, discarded, time.Hour), func() bool { return len(discarded.written) == 1 })

	_, err = lostClient.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()[len(stub.requests())-1].client).To(Equal("operator-bootstrap"))
}
