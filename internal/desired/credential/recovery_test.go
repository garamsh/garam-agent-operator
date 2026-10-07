package credential_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// annotationsOf reads a Secret's annotations.
func annotationsOf(name string) map[string]string {
	GinkgoHelper()

	read := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, read)).To(Succeed())

	return read.Annotations
}

var _ = Describe("Managed credential recovery store", func() {
	It("keeps the request it persisted, and places a recovered pair beside the kept issuer and server root before it removes it", func() {
		grn := "grn:acme:default:agent:9999999999999999"
		managedAgent(grn)
		store := newStore()
		Expect(store.Place(ctx, grn, []byte("first key"), desired.Certificate{
			CertificatePEM: []byte("first certificate"), IssuerPEM: []byte("kept issuer"), ServerRootPEM: []byte("kept root"),
		})).To(Succeed())
		issuer, placed, err := store.KeptIssuer(ctx, grn)
		Expect(err).NotTo(HaveOccurred())
		Expect(placed).To(BeTrue())
		Expect(issuer).To(Equal([]byte("kept issuer")))

		By("persisting a request, which a second one never replaces")
		first := desired.PendingRequest{KeyPEM: []byte("recovery key"), CSRPEM: []byte("recovery csr"), Epoch: "7", ID: "rec-1"}
		Expect(store.SaveRecovery(ctx, grn, first)).To(Succeed())
		Expect(store.SaveRecovery(ctx, grn, desired.PendingRequest{
			KeyPEM: []byte("another key"), CSRPEM: []byte("another csr"), Epoch: "8", ID: "rec-2",
		})).To(Succeed())
		loaded, found, err := store.LoadRecovery(ctx, grn)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loaded).To(Equal(first), "a persisted recovery request was replaced")
		Expect(store.Recovering(ctx)).To(ConsistOf(grn))

		By("recording a refusal on the request, which is kept")
		Expect(store.RefuseRecovery(ctx, grn, agentv1alpha1.ReasonRecoveredCertificateUnverified)).To(Succeed())
		Expect(annotationsOf(agentname.RecoveryRequestSecret(grn))).To(HaveKeyWithValue(
			agentname.RecoveryRefusedAnnotation, agentv1alpha1.ReasonRecoveredCertificateUnverified))

		By("placing the recovered pair, which keeps the issuer and the server root, and then removing the request")
		Expect(store.PlaceRecovered(ctx, grn, []byte("recovery key"),
			desired.Certificate{CertificatePEM: []byte("recovered certificate")}, "lineage-2")).To(Succeed())
		data, _ := secret(agentname.CredentialsSecret(grn))
		Expect(data).To(Equal(map[string][]byte{
			garam.CertificateKey: []byte("recovered certificate"), garam.KeyKey: []byte("recovery key"),
			garam.IssuerKey: []byte("kept issuer"), garam.ServerRootKey: []byte("kept root"),
		}))
		Expect(annotationsOf(agentname.CredentialsSecret(grn))).To(HaveKeyWithValue(
			agentname.CredentialLineageAnnotation, "lineage-2"))
		_, persisted := secret(agentname.RecoveryRequestSecret(grn))
		Expect(persisted).To(BeFalse(), "the request outlived the placed credential")
		Expect(store.Recovering(ctx)).To(BeEmpty())
	})

	It("writes the chain a recovery is answered with in place of the placed one", func() {
		grn := "grn:acme:default:agent:7777777777777777"
		managedAgent(grn)
		store := newStore()
		Expect(store.Place(ctx, grn, []byte("first key"), desired.Certificate{
			CertificatePEM: []byte("first certificate"), IssuerPEM: []byte("kept issuer"), ServerRootPEM: []byte("kept root"),
		})).To(Succeed())

		Expect(store.PlaceRecovered(ctx, grn, []byte("recovery key"), desired.Certificate{
			CertificatePEM: []byte("recovered certificate"), IssuerPEM: []byte("answered issuer"),
			ServerRootPEM: []byte("answered root"),
		}, "lineage-2")).To(Succeed())
		data, _ := secret(agentname.CredentialsSecret(grn))
		Expect(data).To(Equal(map[string][]byte{
			garam.CertificateKey: []byte("recovered certificate"), garam.KeyKey: []byte("recovery key"),
			garam.IssuerKey: []byte("answered issuer"), garam.ServerRootKey: []byte("answered root"),
		}))
	})

	It("creates the credential whole where none was placed, from the answered chain alone", func() {
		grn := "grn:acme:default:agent:6666666666666666"
		managedAgent(grn)
		store := newStore()
		Expect(store.SaveRecovery(ctx, grn, desired.PendingRequest{
			KeyPEM: []byte("recovery key"), CSRPEM: []byte("recovery csr"), Epoch: "7", ID: "rec-1",
		})).To(Succeed())

		By("refusing to create one with no chain answered")
		Expect(store.PlaceRecovered(ctx, grn, []byte("recovery key"),
			desired.Certificate{CertificatePEM: []byte("recovered certificate")}, "lineage-2")).
			To(MatchError(ContainSubstring("no chain is answered")))
		_, placed := secret(agentname.CredentialsSecret(grn))
		Expect(placed).To(BeFalse())

		By("creating it with the answered chain, and then removing the request")
		Expect(store.PlaceRecovered(ctx, grn, []byte("recovery key"), desired.Certificate{
			CertificatePEM: []byte("recovered certificate"), IssuerPEM: []byte("answered issuer"),
			ServerRootPEM: []byte("answered root"),
		}, "lineage-2")).To(Succeed())
		data, placed := secret(agentname.CredentialsSecret(grn))
		Expect(placed).To(BeTrue())
		Expect(data).To(HaveKeyWithValue(garam.IssuerKey, []byte("answered issuer")))
		Expect(annotationsOf(agentname.CredentialsSecret(grn))).To(HaveKeyWithValue(
			agentname.CredentialLineageAnnotation, "lineage-2"))
		_, persisted := secret(agentname.RecoveryRequestSecret(grn))
		Expect(persisted).To(BeFalse())
	})

	It("keeps no issuer for an agent whose first credential is not placed", func() {
		grn := "grn:acme:default:agent:8888888888888888"
		managedAgent(grn)
		_, placed, err := newStore().KeptIssuer(ctx, grn)
		Expect(err).NotTo(HaveOccurred())
		Expect(placed).To(BeFalse())
	})
})
