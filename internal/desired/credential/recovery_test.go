package credential_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/desired/credential"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// namespace is the spec's own, so one spec's Secrets are never another's.
var namespace string

var _ = BeforeEach(func() {
	namespaces++
	namespace = fmt.Sprintf("recovery-%d", namespaces)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
})

var namespaces int

// managedAgent creates grn's Agent on the control source.
func managedAgent(grn string) {
	GinkgoHelper()

	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(grn), Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 "example.com/sherlock:v1",
			CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize:           resource.MustParse("1Gi"),
			Identity:              &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7"},
		},
	}
	Expect(k8sClient.Create(ctx, agent)).To(Succeed())
}

// placeTokens writes grn's credential as the refresher leaves it: its tokens,
// due at a refresh time.
func placeTokens(grn string) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: agentname.CredentialsSecret(grn), Namespace: namespace,
			Annotations: map[string]string{agentname.TokensRefreshAnnotation: "2026-10-14T15:40:00Z"},
		},
		Data: map[string][]byte{garam.TokenKey: []byte("a token"), garam.ServerRootKey: []byte("a root")},
	})).To(Succeed())
}

// secret reads a Secret's data and annotations, and reports false where it
// does not exist.
func secret(name string) (map[string][]byte, map[string]string, bool) {
	GinkgoHelper()

	read := &corev1.Secret{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, read)
	if apierrors.IsNotFound(err) {
		return nil, nil, false
	}
	Expect(err).NotTo(HaveOccurred())

	return read.Data, read.Annotations, true
}

func newStore() *credential.Secrets {
	return credential.NewSecrets(k8sClient, k8sClient, scheme.Scheme, namespace)
}

var _ = Describe("Managed credential recovery store", func() {
	It("keeps the request it persisted, and records the new lineage so the tokens are minted again before it removes it", func() {
		grn := "grn:acme:default:agent:9999999999999999"
		managedAgent(grn)
		placeTokens(grn)
		store := newStore()

		By("persisting a request, which a second one never replaces")
		first := desired.RecoveryRequest{Epoch: "7", ID: "rec-1"}
		Expect(store.SaveRecovery(ctx, grn, first)).To(Succeed())
		Expect(store.SaveRecovery(ctx, grn, desired.RecoveryRequest{Epoch: "8", ID: "rec-2"})).To(Succeed())
		loaded, found, err := store.LoadRecovery(ctx, grn)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(loaded).To(Equal(first), "a persisted recovery request was replaced")
		Expect(store.Recovering(ctx)).To(ConsistOf(grn))
		persisted, _, _ := secret(agentname.RecoveryRequestSecret(grn))
		Expect(persisted).To(Equal(map[string][]byte{"epoch": []byte("7"), "request-id": []byte("rec-1")}),
			"the request carries more than garam's recovery is sent")

		By("recording the lineage, removing the refresh time, and then removing the request")
		Expect(store.Recovered(ctx, grn, "lineage-2")).To(Succeed())
		data, annotations, _ := secret(agentname.CredentialsSecret(grn))
		Expect(annotations).To(HaveKeyWithValue(agentname.CredentialLineageAnnotation, "lineage-2"))
		Expect(annotations).NotTo(HaveKey(agentname.TokensRefreshAnnotation),
			"the tokens of the old lineage are not due at once")
		Expect(data).To(HaveKeyWithValue(garam.TokenKey, []byte("a token")), "the recovery wrote the tokens itself")
		_, _, persistedAfter := secret(agentname.RecoveryRequestSecret(grn))
		Expect(persistedAfter).To(BeFalse(), "the request outlived the recorded lineage")
		Expect(store.Recovering(ctx)).To(BeEmpty())
	})

	It("records nothing on an agent with no credential placed, and removes its request", func() {
		grn := "grn:acme:default:agent:6666666666666666"
		managedAgent(grn)
		store := newStore()
		Expect(store.SaveRecovery(ctx, grn, desired.RecoveryRequest{Epoch: "7", ID: "rec-1"})).To(Succeed())

		Expect(store.Recovered(ctx, grn, "lineage-2")).To(Succeed())
		_, _, placed := secret(agentname.CredentialsSecret(grn))
		Expect(placed).To(BeFalse(), "a credential was made with no tokens in it")
		_, _, persisted := secret(agentname.RecoveryRequestSecret(grn))
		Expect(persisted).To(BeFalse())
	})
})
