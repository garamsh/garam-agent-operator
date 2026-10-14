package constructor_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/garam/constructor"
)

// tokensNamespace is the spec's own, so one spec's agents are never another's.
var tokensNamespace string

var tokenSpecs int

var _ = BeforeEach(func() {
	tokenSpecs++
	tokensNamespace = fmt.Sprintf("tokens-%d", tokenSpecs)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: tokensNamespace}})).To(Succeed())
})

// agentWith creates the Agent for grn with identity, nil for one a user wrote.
func agentWith(grn string, identity *agentv1alpha1.AgentIdentity) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(grn), Namespace: tokensNamespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 "example.com/sherlock:v1",
			CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize:           resource.MustParse("1Gi"),
			Identity:              identity,
		},
	})).To(Succeed())
}

// credentialOf reads grn's credential Secret.
func credentialOf(grn string) *corev1.Secret {
	GinkgoHelper()

	read := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tokensNamespace, Name: agentname.CredentialsSecret(grn)},
		read)).To(Succeed())

	return read
}

// holderOf is grn's holder as the store lists it.
func holderOf(store *constructor.Tokens, grn string) garam.TokenHolder {
	GinkgoHelper()

	holders, err := store.Holders(ctx)
	Expect(err).NotTo(HaveOccurred())
	for _, holder := range holders {
		if holder.Agent == garam.GRN(grn) {
			return holder
		}
	}
	Fail("the store lists no holder for " + grn)

	return garam.TokenHolder{}
}

// tokensOf is what one refresh mints for an agent, the control token where
// fenced.
func tokensOf(token string, fenced bool) garam.Tokens {
	tokens := garam.Tokens{Garam: garam.AgentToken{Token: token, ServerRootPEM: []byte("a root")}}
	if fenced {
		tokens.Control = &garam.AgentToken{Token: "control " + token}
	}

	return tokens
}

var _ = Describe("Token store", func() {
	It("lists every agent with an identity, on either source, and no agent a user wrote", func() {
		managed, claimed, written := "grn:acme:default:agent:managed", "grn:acme:default:agent:claimed", "written"
		agentWith(managed, &agentv1alpha1.AgentIdentity{
			GRN: managed, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl,
		})
		agentWith(claimed, &agentv1alpha1.AgentIdentity{GRN: claimed, Source: agentv1alpha1.DesiredSourceGaram})
		agentWith(written, nil)

		holders, err := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace).Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(
			garam.TokenHolder{Agent: garam.GRN(managed), Epoch: 7, Fenced: true},
			garam.TokenHolder{Agent: garam.GRN(claimed)},
		))
	})

	It("creates the credential owned by the agent's Agent, then replaces its tokens and refresh time in one write", func() {
		grn := "grn:acme:default:agent:fenced"
		agentWith(grn, &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl})
		store := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace)
		due := time.Date(2026, 10, 14, 15, 40, 0, 0, time.UTC)

		By("creating it where none is placed")
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("first", true), due)).To(Succeed())
		created := credentialOf(grn)
		Expect(created.Data).To(Equal(map[string][]byte{
			garam.TokenKey: []byte("first"), garam.ControlTokenKey: []byte("control first"), garam.ServerRootKey: []byte("a root"),
		}))
		Expect(created.OwnerReferences).To(HaveLen(1))
		Expect(created.OwnerReferences[0].Name).To(Equal(agentname.Agent(grn)))
		Expect(holderOf(store, grn).Due).To(Equal(due))

		By("replacing the tokens and the refresh time together, keeping the lineage a recovery recorded")
		Expect(k8sClient.Patch(ctx, created, client.RawPatch(types.MergePatchType,
			[]byte(`{"metadata":{"annotations":{"`+agentname.CredentialLineageAnnotation+`":"lineage-2"}}}`)))).To(Succeed())
		later := due.Add(time.Hour)
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("second", true), later)).To(Succeed())
		replaced := credentialOf(grn)
		Expect(replaced.Data).To(Equal(map[string][]byte{
			garam.TokenKey: []byte("second"), garam.ControlTokenKey: []byte("control second"), garam.ServerRootKey: []byte("a root"),
		}))
		Expect(replaced.Annotations).To(HaveKeyWithValue(agentname.CredentialLineageAnnotation, "lineage-2"))
		Expect(holderOf(store, grn).Due).To(Equal(later))
	})

	It("refuses to write over a credential a recovery changed since it was read", func() {
		grn := "grn:acme:default:agent:recovered"
		agentWith(grn, &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl})
		store := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace)
		due := time.Date(2026, 10, 14, 15, 40, 0, 0, time.UTC)
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("old lineage", true), due)).To(Succeed())

		read := holderOf(store, grn)
		// The recoverer records the new lineage and removes the refresh time
		// between the read and the write, as credential.Secrets does.
		Expect(k8sClient.Patch(ctx, credentialOf(grn), client.RawPatch(types.MergePatchType, []byte(
			`{"metadata":{"annotations":{"`+agentname.CredentialLineageAnnotation+`":"lineage-2","`+
				agentname.TokensRefreshAnnotation+`":null}}}`)))).To(Succeed())

		err := store.Store(ctx, read, tokensOf("minted before the recovery", true), due.Add(time.Hour))
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "a write over the recovered credential was taken: %v", err)
		Expect(credentialOf(grn).Data[garam.TokenKey]).To(Equal([]byte("old lineage")))
		Expect(holderOf(store, grn).Due).To(BeZero(), "the recovered agent is not due at once")

		// Control: the same write under the version read after the recovery is taken.
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("new lineage", true), due.Add(time.Hour))).To(Succeed())
		Expect(credentialOf(grn).Data[garam.TokenKey]).To(Equal([]byte("new lineage")))
	})
})
