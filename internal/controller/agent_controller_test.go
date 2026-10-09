package controller

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

const agentNamespace = "default"

// agentNameLimit is the longest Agent name the CRD accepts, and the number the
// marker on the type carries. The workload's Pods label themselves with the
// Agent's name and a suffix of up to 11 characters, and a label value stops at
// 63 bytes.
const agentNameLimit = 52

// credentialsSecretName is the Secret the baseline Agent of that name expects.
func credentialsSecretName(agent string) string {
	return agent + "-credentials"
}

// testGRN is the GRN garam minted for an agent this operator constructed.
const testGRN = "grn:acme:default:agent:9f2ac1b40d8e7a35"

// modelKeySecretName is the Secret the model of a baseline Agent of that name
// takes its key from.
func modelKeySecretName(agent string) string {
	return agent + "-model-key"
}

// newModel returns a model the API server accepts for the Agent of that name,
// with the values a sherlock agent was measured answering with through the
// tool-calling path.
func newModel(agent string) *agentv1alpha1.ModelSpec {
	return &agentv1alpha1.ModelSpec{
		Provider:        "openai-compatible",
		BaseURL:         "https://api.minimax.io/v1",
		Name:            "MiniMax-M2",
		APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: modelKeySecretName(agent), Key: "api-key"},
		Embedding: &agentv1alpha1.EmbeddingSpec{
			BaseURL:         testEmbeddingBaseURL,
			Name:            testEmbeddingModel,
			APIKeySecretRef: &agentv1alpha1.SecretKeyReference{Name: modelKeySecretName(agent), Key: "embedding-key"},
		},
	}
}

const (
	testEmbeddingBaseURL = "https://embeddings.example/v1"
	testEmbeddingModel   = "bge-base-en-v1.5"
	testMockProvider     = "mock"
)

// newAgent returns an Agent the API server accepts, so that a spec differing
// from it in one field isolates that field.
func newAgent(name string) *agentv1alpha1.Agent {
	return &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: agentNamespace,
		},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 "example.com/sherlock:v0.1.0",
			CredentialsSecretName: credentialsSecretName(name),
			StorageSize:           resource.MustParse("1Gi"),
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
		},
	}
}

// createSecret creates the credentials Secret an Agent names, and deletes it
// when the spec ends.
func createSecret(name string) {
	GinkgoHelper()

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agentNamespace}}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	DeferCleanup(func() {
		Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
	})
}

// createAgent creates an Agent, and when the spec ends deletes it along with the
// StatefulSet it owns: envtest runs no garbage collector, so an owned object
// outlives its owner here.
func createAgent(agent *agentv1alpha1.Agent) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, agent)).To(Succeed())
	DeferCleanup(func() {
		// No controller runs here to release the Agent's workload fence, so the
		// spec releases it to let the Agent go.
		releaseFinalizers(agent)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, agent))).To(Succeed())

		workload := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, workload))).To(Succeed())
	})
}

// testCopyImage is what the specs expect on the credential's init container.
const testCopyImage = "example.com/copy:v0.1.0"

// testWorkspaceImage is the image the specs expect an agent's workspace
// container to run, where this operator names one.
const testWorkspaceImage = "example.com/workspace:v0.1.0"

// releaseFinalizers removes every finalizer from obj as the API server now holds
// it, where it still exists.
func releaseFinalizers(obj client.Object) {
	GinkgoHelper()

	current := obj.DeepCopyObject().(client.Object)
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
		Expect(client.IgnoreNotFound(err)).To(Succeed())

		return
	}
	released := current.DeepCopyObject().(client.Object)
	released.SetFinalizers(nil)
	Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, released, client.MergeFrom(current)))).To(Succeed())
}

// reconcileAgent runs one reconcile for the named Agent, with this operator
// naming no workspace image.
func reconcileAgent(name string) (reconcile.Result, error) {
	return reconcileAgentWith(name, "")
}

// reconcileAgentWithWorkspace runs one reconcile for the named Agent, with this
// operator running agents' workspace from testWorkspaceImage.
func reconcileAgentWithWorkspace(name string) (reconcile.Result, error) {
	return reconcileAgentWith(name, testWorkspaceImage)
}

// reconcileAgentWith runs one reconcile for the named Agent, with the images
// this operator's own configuration carries.
func reconcileAgentWith(name, workspaceImage string) (reconcile.Result, error) {
	return runReconcile(name, &AgentReconciler{
		Client:         k8sClient,
		Scheme:         k8sClient.Scheme(),
		CopyImage:      testCopyImage,
		WorkspaceImage: workspaceImage,
	})
}

// testAdapterImage is the image the specs expect garam's adapter to run, where
// this operator names one, and testGaramAddress the machine listener it claims
// from.
const (
	testAdapterImage = "example.com/garam:v0.1.0"
	testGaramAddress = "garam-machine.garam.svc:8443"
)

// reconcileAgentWithAdapter runs one reconcile for the named Agent, with this
// operator placing garam's adapter from testAdapterImage.
func reconcileAgentWithAdapter(name string) (reconcile.Result, error) {
	return runReconcile(name, &AgentReconciler{
		Client:       k8sClient,
		Scheme:       k8sClient.Scheme(),
		CopyImage:    testCopyImage,
		AdapterImage: testAdapterImage,
		GaramAddress: testGaramAddress,
	})
}

// runReconcile runs one reconcile for the named Agent through reconciler.
func runReconcile(name string, reconciler *AgentReconciler) (reconcile.Result, error) {
	// envtest's client reads straight from the API server, which is what the
	// manager's API reader does.
	if reconciler.APIReader == nil {
		reconciler.APIReader = k8sClient
	}

	return reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: agentNamespace},
	})
}

// readAgent reads an Agent back as the API server now holds it. A reconcile
// writes the Agent's status, so a copy taken before one is stale and an update
// through it is refused.
func readAgent(name string) *agentv1alpha1.Agent {
	GinkgoHelper()

	agent := &agentv1alpha1.Agent{}
	key := types.NamespacedName{Name: name, Namespace: agentNamespace}
	Expect(k8sClient.Get(ctx, key, agent)).To(Succeed())

	return agent
}

// statefulSetFor reads back the StatefulSet an Agent of that name owns.
func statefulSetFor(name string) *appsv1.StatefulSet {
	GinkgoHelper()

	workload := &appsv1.StatefulSet{}
	key := types.NamespacedName{Name: name, Namespace: agentNamespace}
	Expect(k8sClient.Get(ctx, key, workload)).To(Succeed())

	return workload
}

var _ = Describe("Agent", func() {
	It("accepts a name as long as its workload's labels allow, and refuses one character more", func() {
		By("creating an Agent whose name is exactly at the bound")
		accepted := newAgent(strings.Repeat("a", agentNameLimit))
		Expect(k8sClient.Create(ctx, accepted)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, accepted)).To(Succeed())
		})

		By("creating one that differs from it by a single character of name")
		rejected := newAgent(strings.Repeat("a", agentNameLimit+1))
		err := k8sClient.Create(ctx, rejected)
		Expect(err).To(MatchError(ContainSubstring(
			fmt.Sprintf("metadata.name must be %d characters or fewer", agentNameLimit))))
	})

	It("stores the spec it was created with, and refuses an image the CRD cannot accept", func() {
		By("creating an Agent the API server accepts")
		accepted := newAgent("stores-its-spec")
		Expect(k8sClient.Create(ctx, accepted)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, accepted)).To(Succeed())
		})

		By("reading the Agent back")
		readBack := &agentv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(accepted), readBack)).To(Succeed())
		Expect(readBack.Spec.Image).To(Equal("example.com/sherlock:v0.1.0"))
		Expect(readBack.Spec.CredentialsSecretName).To(Equal(credentialsSecretName("stores-its-spec")))
		Expect(readBack.Spec.StorageSize.String()).To(Equal("1Gi"))

		By("creating the same Agent with an empty image")
		rejected := newAgent("empty-image")
		rejected.Spec.Image = ""
		err := k8sClient.Create(ctx, rejected)
		Expect(err).To(MatchError(ContainSubstring("spec.image")))
	})

	It("refuses a model missing a setting, which would leave the agent on its own default for it", func() {
		By("creating an Agent naming a whole model")
		accepted := newAgent("names-a-whole-model")
		accepted.Spec.Model = newModel(accepted.Name)
		Expect(k8sClient.Create(ctx, accepted)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, accepted)).To(Succeed())
		})

		By("creating one that differs from it by the endpoint alone")
		rejected := newAgent("names-a-model-without-endpoint")
		rejected.Spec.Model = newModel(rejected.Name)
		rejected.Spec.Model.BaseURL = ""
		Expect(k8sClient.Create(ctx, rejected)).To(MatchError(ContainSubstring("spec.model.baseURL")))
	})

	It("refuses a model other than mock that names no embeddings endpoint, and admits the mock with none", func() {
		By("the control: the same model naming one")
		accepted := newAgent("names-an-embedding")
		accepted.Spec.Model = newModel(accepted.Name)
		Expect(k8sClient.Create(ctx, accepted)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, accepted)).To(Succeed())
		})

		rejected := newAgent("names-no-embedding")
		rejected.Spec.Model = newModel(rejected.Name)
		rejected.Spec.Model.Embedding = nil
		Expect(k8sClient.Create(ctx, rejected)).To(MatchError(ContainSubstring("embedding is required")))

		By("the mock, which sherlock runs with no embeddings endpoint")
		mock := newAgent("mock-names-no-embedding")
		mock.Spec.Model = newModel(mock.Name)
		mock.Spec.Model.Provider, mock.Spec.Model.Embedding = testMockProvider, nil
		Expect(k8sClient.Create(ctx, mock)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Delete(ctx, mock)).To(Succeed())
		})
	})

	It("refuses changing the embedding model or endpoint once set, and accepts changing its key", func() {
		agent := newAgent("keeps-its-embedding")
		agent.Spec.Model = newModel(agent.Name)
		createAgent(agent)

		By("the control: moving the key to another Secret")
		rekeyed := readAgent(agent.Name)
		rekeyed.Spec.Model.Embedding.APIKeySecretRef = &agentv1alpha1.SecretKeyReference{Name: "embeddings", Key: "key"}
		Expect(k8sClient.Update(ctx, rekeyed)).To(Succeed())

		for field, change := range map[string]func(*agentv1alpha1.EmbeddingSpec){
			"name":    func(e *agentv1alpha1.EmbeddingSpec) { e.Name = "text-embedding-3-small" },
			"baseURL": func(e *agentv1alpha1.EmbeddingSpec) { e.BaseURL = "https://other.example/v1" },
		} {
			changed := readAgent(agent.Name)
			change(changed.Spec.Model.Embedding)
			Expect(k8sClient.Update(ctx, changed)).To(MatchError(ContainSubstring("cannot be changed or removed once set")),
				"changing %s", field)
		}
		Expect(readAgent(agent.Name).Spec.Model.Embedding).To(Equal(rekeyed.Spec.Model.Embedding))

		By("removing the embedding, or the model carrying it, which would let the next update change it")
		withoutEmbedding := readAgent(agent.Name)
		withoutEmbedding.Spec.Model.Provider, withoutEmbedding.Spec.Model.Embedding = testMockProvider, nil
		Expect(k8sClient.Update(ctx, withoutEmbedding)).To(MatchError(ContainSubstring("cannot be changed or removed once set")))
		withoutModel := readAgent(agent.Name)
		withoutModel.Spec.Model = nil
		Expect(k8sClient.Update(ctx, withoutModel)).To(MatchError(ContainSubstring("cannot be changed or removed once set")))
		Expect(readAgent(agent.Name).Spec.Model).NotTo(BeNil())
	})

	It("lets a mock model with no embedding gain one and be removed while it has none", func() {
		agent := newAgent("gains-an-embedding")
		agent.Spec.Model = newModel(agent.Name)
		agent.Spec.Model.Provider, agent.Spec.Model.Embedding = testMockProvider, nil
		createAgent(agent)

		By("removing the mock model, which no stored vector depends on")
		removed := readAgent(agent.Name)
		removed.Spec.Model = nil
		Expect(k8sClient.Update(ctx, removed)).To(Succeed())

		By("declaring a model with its embedding, which sets it for the first time")
		gained := readAgent(agent.Name)
		gained.Spec.Model = newModel(agent.Name)
		Expect(k8sClient.Update(ctx, gained)).To(Succeed())
		Expect(readAgent(agent.Name).Spec.Model.Embedding).NotTo(BeNil())
	})

	It("refuses changing or removing an identity's GRN, and accepts setting one and moving its epoch", func() {
		agent := newAgent("keeps-its-grn")
		createAgent(agent)

		By("setting an identity on an Agent carrying none, which is what the upgrade does")
		withIdentity := readAgent(agent.Name)
		withIdentity.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, AssignmentEpoch: "7"}
		Expect(k8sClient.Update(ctx, withIdentity)).To(Succeed())

		By("moving the epoch alone, which the writer of the identity does")
		movedEpoch := readAgent(agent.Name)
		movedEpoch.Spec.Identity.AssignmentEpoch = "8"
		Expect(k8sClient.Update(ctx, movedEpoch)).To(Succeed())

		By("changing the GRN")
		changed := readAgent(agent.Name)
		changed.Spec.Identity.GRN = "grn:acme:default:agent:0a1b2c3d4e5f6071"
		Expect(k8sClient.Update(ctx, changed)).
			To(MatchError(ContainSubstring("identity.grn cannot be changed or removed once set")))

		By("removing the identity")
		removed := readAgent(agent.Name)
		removed.Spec.Identity = nil
		Expect(k8sClient.Update(ctx, removed)).
			To(MatchError(ContainSubstring("identity.grn cannot be changed or removed once set")))

		Expect(readAgent(agent.Name).Spec.Identity).To(Equal(
			&agentv1alpha1.AgentIdentity{GRN: testGRN, AssignmentEpoch: "8"}))
	})

	It("lets an identity's source move from Garam to Control and never back", func() {
		agent := newAgent("moves-its-source")
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, Source: agentv1alpha1.DesiredSourceGaram}
		createAgent(agent)

		By("the control: switching from Garam to Control, which the #1171 switch does")
		switched := readAgent(agent.Name)
		switched.Spec.Identity.Source = agentv1alpha1.DesiredSourceControl
		Expect(k8sClient.Update(ctx, switched)).To(Succeed())

		By("moving back to Garam")
		back := readAgent(agent.Name)
		back.Spec.Identity.Source = agentv1alpha1.DesiredSourceGaram
		Expect(k8sClient.Update(ctx, back)).To(MatchError(ContainSubstring("identity.source cannot leave Control once set")))

		By("clearing it, which would read as Garam")
		cleared := readAgent(agent.Name)
		cleared.Spec.Identity.Source = ""
		Expect(k8sClient.Update(ctx, cleared)).To(MatchError(ContainSubstring("identity.source cannot leave Control once set")))

		Expect(readAgent(agent.Name).Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceControl))
	})

	It("admits a revision only on a Control-source Agent, and only as a canonical decimal string", func() {
		By("the control: a Control-source Agent carrying a canonical revision")
		agent := newAgent("carries-a-revision")
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, Source: agentv1alpha1.DesiredSourceControl}
		agent.Spec.Revision = "9223372036854775807"
		createAgent(agent)
		Expect(readAgent(agent.Name).Spec.Revision).To(Equal("9223372036854775807"))

		for _, revision := range []string{"0", "01", "-1", "+1", "1.0", "one", "92233720368547758070"} {
			malformed := readAgent(agent.Name)
			malformed.Spec.Revision = revision
			Expect(k8sClient.Update(ctx, malformed)).To(MatchError(ContainSubstring("spec.revision")), "revision %q", revision)
		}

		for _, identity := range []*agentv1alpha1.AgentIdentity{
			nil,
			{GRN: testGRN},
			{GRN: testGRN, Source: agentv1alpha1.DesiredSourceGaram},
		} {
			elsewhere := newAgent("carries-a-revision-elsewhere")
			elsewhere.Spec.Identity = identity
			elsewhere.Spec.Revision = "1"
			Expect(k8sClient.Create(ctx, elsewhere)).
				To(MatchError(ContainSubstring("revision is set only on an agent whose identity.source is Control")),
					"identity %+v", identity)
		}
	})

	It("reports a storage class the claimed volume cannot change to, beside an unchanged one", func() {
		name := "changes-its-storage-class"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		By("the control: the class the volume was claimed with")
		synced := meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionSynced)
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))

		By("asking for another class")
		edited := readAgent(name)
		edited.Spec.StorageClassName = ptr.To("fast")
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		synced = meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionSynced)
		Expect(synced.Status).To(Equal(metav1.ConditionFalse))
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonStorageClassImmutable))
		Expect(statefulSetFor(name).Spec.VolumeClaimTemplates[0].Spec.StorageClassName).To(BeNil())
	})

	It("reconciles an Agent that is gone without returning an error", func() {
		result, err := reconcileAgent("never-created")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
	})

	It("builds nothing until the credentials Secret exists", func() {
		name := "waits-for-credentials"
		createAgent(newAgent(name))

		By("reconciling while the Secret is absent")
		result, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		key := types.NamespacedName{Name: name, Namespace: agentNamespace}
		Expect(k8sClient.Get(ctx, key, &appsv1.StatefulSet{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))

		By("reconciling once the Secret exists")
		createSecret(credentialsSecretName(name))
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.Containers).To(HaveLen(1))
	})

	It("builds nothing until the Secret holding the model's key exists", func() {
		name := "waits-for-model-key"
		// The control: the credentials Secret exists, so what holds the
		// workload back is the model's key and nothing before it.
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Model = newModel(name)
		createAgent(agent)

		By("reconciling while the key's Secret is absent")
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		key := types.NamespacedName{Name: name, Namespace: agentNamespace}
		Expect(k8sClient.Get(ctx, key, &appsv1.StatefulSet{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))
		synced := meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionSynced)
		Expect(synced).NotTo(BeNil())
		Expect(synced.Status).To(Equal(metav1.ConditionFalse))
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonModelKeySecretMissing))

		By("reconciling once it exists")
		createSecret(modelKeySecretName(name))
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.Containers).To(HaveLen(1))
	})

	It("builds nothing until the Secret holding the embeddings endpoint's key exists", func() {
		name := "waits-for-embedding-key"
		// The control: the credentials and the model's key exist, so what holds
		// the workload back is the embeddings endpoint's key alone.
		createSecret(credentialsSecretName(name))
		createSecret(modelKeySecretName(name))
		agent := newAgent(name)
		agent.Spec.Model = newModel(name)
		agent.Spec.Model.Embedding.APIKeySecretRef = &agentv1alpha1.SecretKeyReference{Name: name + "-embedding", Key: "key"}
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		key := types.NamespacedName{Name: name, Namespace: agentNamespace}
		Expect(k8sClient.Get(ctx, key, &appsv1.StatefulSet{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))
		synced := meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionSynced)
		Expect(synced).NotTo(BeNil())
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonEmbeddingKeySecretMissing))

		createSecret(name + "-embedding")
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.Containers).To(HaveLen(1))
	})

	It("wakes an Agent whose model names an arriving Secret", func() {
		waiting := newAgent("names-the-model-key")
		waiting.Spec.Model = newModel(waiting.Name)
		createAgent(waiting)

		// The control: an Agent naming no model is not woken by the same Secret.
		createAgent(newAgent("names-no-model-key"))

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: modelKeySecretName(waiting.Name), Namespace: agentNamespace},
		}

		reconciler := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		Expect(reconciler.agentsNamingSecret(ctx, secret)).To(ConsistOf(reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(waiting),
		}))
	})

	It("wakes the Agents that name an arriving Secret, and no others", func() {
		waiting := newAgent("names-the-secret")
		createAgent(waiting)

		other := newAgent("names-another-secret")
		createAgent(other)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      credentialsSecretName(waiting.Name),
				Namespace: agentNamespace,
			},
		}

		reconciler := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		Expect(reconciler.agentsNamingSecret(ctx, secret)).To(ConsistOf(reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(waiting),
		}))
	})
})
