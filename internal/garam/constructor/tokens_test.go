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

// tokensOf is what one refresh mints for a fenced agent: its token, and the one
// for the control service.
func tokensOf(token string) garam.Tokens {
	return garam.Tokens{
		Garam:   garam.AgentToken{Token: token, ServerRootPEM: []byte("a root")},
		Control: &garam.AgentToken{Token: "control " + token},
	}
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

	It("lists an agent whose epoch does not parse beside the others, so the others are still refreshed", func() {
		malformed, sound := "grn:acme:default:agent:malformed", "grn:acme:default:agent:sound"
		agentWith(malformed, &agentv1alpha1.AgentIdentity{GRN: malformed, AssignmentEpoch: "x", Source: agentv1alpha1.DesiredSourceGaram})
		agentWith(sound, &agentv1alpha1.AgentIdentity{GRN: sound, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceGaram})

		holders, err := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace).Holders(ctx)
		Expect(err).NotTo(HaveOccurred(), "one agent's epoch failed the whole list")
		Expect(holders).To(HaveLen(2))
		for _, holder := range holders {
			switch holder.Agent {
			case garam.GRN(sound):
				// Control: the sound agent is listed as it would be alone.
				Expect(holder.Err).NotTo(HaveOccurred())
				Expect(holder.Epoch).To(Equal(int64(7)))
			case garam.GRN(malformed):
				Expect(holder.Err).To(MatchError(garam.ErrEpochUnreadable))
				Expect(holder.Err).To(MatchError(ContainSubstring(malformed)))
			}
		}
	})

	It("creates the credential owned by the agent's Agent, then replaces its tokens and refresh time in one write", func() {
		grn := "grn:acme:default:agent:fenced"
		agentWith(grn, &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl})
		store := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace)
		due := time.Date(2026, 10, 14, 15, 40, 0, 0, time.UTC)

		By("creating it where none is placed")
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("first"), due)).To(Succeed())
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
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("second"), later)).To(Succeed())
		replaced := credentialOf(grn)
		Expect(replaced.Data).To(Equal(map[string][]byte{
			garam.TokenKey: []byte("second"), garam.ControlTokenKey: []byte("control second"), garam.ServerRootKey: []byte("a root"),
		}))
		Expect(replaced.Annotations).To(HaveKeyWithValue(agentname.CredentialLineageAnnotation, "lineage-2"))
		Expect(holderOf(store, grn).Due).To(Equal(later))
	})

	It("mints at once for an agent a cutover fenced, whose credential holds no token for the control service", func() {
		grn := "grn:acme:default:agent:cutover"
		agentWith(grn, &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceGaram})
		store := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace)
		due := time.Date(2026, 10, 14, 15, 40, 0, 0, time.UTC)
		unfenced := tokensOf("garam's alone")
		unfenced.Control = nil
		Expect(store.Store(ctx, holderOf(store, grn), unfenced, due)).To(Succeed())
		Expect(holderOf(store, grn).Due).To(Equal(due), "the unfenced agent's tokens are not due at their refresh time")

		By("moving the agent to the control service, as a cutover does")
		agent := &agentv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tokensNamespace, Name: agentname.Agent(grn)}, agent)).To(Succeed())
		agent.Spec.Identity.Source = agentv1alpha1.DesiredSourceControl
		Expect(k8sClient.Update(ctx, agent)).To(Succeed())
		moved := holderOf(store, grn)
		Expect(moved.Fenced).To(BeTrue())
		Expect(moved.Due).To(BeZero(), "a fenced agent with no control token waits for its refresh time")

		// Control: once its control token is written, it is due at its refresh time again.
		Expect(store.Store(ctx, moved, tokensOf("fenced"), due)).To(Succeed())
		Expect(holderOf(store, grn).Due).To(Equal(due))
	})

	It("refuses to write over a credential a recovery changed since it was read", func() {
		grn := "grn:acme:default:agent:recovered"
		agentWith(grn, &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl})
		store := constructor.NewTokens(k8sClient, scheme.Scheme, tokensNamespace)
		due := time.Date(2026, 10, 14, 15, 40, 0, 0, time.UTC)
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("old lineage"), due)).To(Succeed())

		read := holderOf(store, grn)
		// The recoverer records the new lineage and removes the refresh time
		// between the read and the write, as credential.Secrets does.
		Expect(k8sClient.Patch(ctx, credentialOf(grn), client.RawPatch(types.MergePatchType, []byte(
			`{"metadata":{"annotations":{"`+agentname.CredentialLineageAnnotation+`":"lineage-2","`+
				agentname.TokensRefreshAnnotation+`":null}}}`)))).To(Succeed())

		err := store.Store(ctx, read, tokensOf("minted before the recovery"), due.Add(time.Hour))
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "a write over the recovered credential was taken: %v", err)
		Expect(credentialOf(grn).Data[garam.TokenKey]).To(Equal([]byte("old lineage")))
		Expect(holderOf(store, grn).Due).To(BeZero(), "the recovered agent is not due at once")

		// Control: the same write under the version read after the recovery is taken.
		Expect(store.Store(ctx, holderOf(store, grn), tokensOf("new lineage"), due.Add(time.Hour))).To(Succeed())
		Expect(credentialOf(grn).Data[garam.TokenKey]).To(Equal([]byte("new lineage")))
	})
})
