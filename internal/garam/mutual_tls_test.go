package garam_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// answerNoDefinitions is what a listener with nothing to say answers a read.
func answerNoDefinitions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`[]`))
}

func TestMutualTLSPresentsTheCertificateOnDiskAtEachCall(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerNoDefinitions)
	dir := t.TempDir()

	certificateFile, keyFile := writeIdentity(t, dir, "operator-before-renewal")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	client := garam.NewClient(stub.address(), tlsConfig)

	_, err = client.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	writeIdentity(t, dir, "operator-after-renewal")
	_, err = client.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(stub.requests()).To(HaveLen(2))
	g.Expect(stub.requests()[0].client).To(Equal("operator-before-renewal"))
	g.Expect(stub.requests()[1].client).To(Equal("operator-after-renewal"))
}

func TestMutualTLSVerifiesTheListenerAgainstTheTrustFileAlone(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerNoDefinitions)
	dir := t.TempDir()
	certificateFile, keyFile := writeIdentity(t, dir, "operator")

	// The listener's own certificate is what verifies it, which is what says
	// the refusal below is the verification and not the connection.
	trusted, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = garam.NewClient(stub.address(), trusted).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	unrelatedPEM, _ := newCertificate(t, "not-garam")
	unrelated, err := garam.MutualTLS(certificateFile, keyFile, writeFile(t, dir, "unrelated.pem", unrelatedPEM))
	g.Expect(err).NotTo(HaveOccurred())
	_, err = garam.NewClient(stub.address(), unrelated).ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("certificate signed by unknown authority")))
}

func TestMutualTLSRefusesConfigurationItCannotRead(t *testing.T) {
	g := NewWithT(t)
	dir := t.TempDir()
	certificateFile, keyFile := writeIdentity(t, dir, "operator")
	listenerPEM, _ := newCertificate(t, "garam-machine")
	trustFile := writeFile(t, dir, "trust.pem", listenerPEM)

	// The configuration that is accepted, so that each refusal below is read
	// against a case that differs from it in one input.
	_, err := garam.MutualTLS(certificateFile, keyFile, trustFile)
	g.Expect(err).NotTo(HaveOccurred())

	_, err = garam.MutualTLS(certificateFile, keyFile, filepath.Join(dir, "absent.pem"))
	g.Expect(err).To(HaveOccurred())

	notACertificate := writeFile(t, dir, "prose.pem", []byte("this is not a certificate\n"))
	_, err = garam.MutualTLS(certificateFile, keyFile, notACertificate)
	g.Expect(err).To(MatchError(ContainSubstring(notACertificate)))

	_, err = garam.MutualTLS(filepath.Join(dir, "absent-certificate.pem"), keyFile, trustFile)
	g.Expect(err).To(HaveOccurred())
}
