package controller

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, agent))).To(Succeed())

		workload := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace},
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, workload))).To(Succeed())
	})
}

// testCopyImage is what the specs expect on the credential's init container.
const testCopyImage = "example.com/copy:v0.1.0"

// testToolsImage is the image the specs expect an agent's tool tree to be
// mounted from, where this operator names one.
const testToolsImage = "example.com/tools:v0.1.0"

// testWorkspaceImage is the image the specs expect an agent's workspace
// container to run, where this operator names one.
const testWorkspaceImage = "example.com/workspace:v0.1.0"

// reconcileAgent runs one reconcile for the named Agent, with this operator
// naming neither a tools image nor a workspace image.
func reconcileAgent(name string) (reconcile.Result, error) {
	return reconcileAgentWith(name, "", "")
}

// reconcileAgentWithTools runs one reconcile for the named Agent, with this
// operator carrying agents' tool tree in testToolsImage.
func reconcileAgentWithTools(name string) (reconcile.Result, error) {
	return reconcileAgentWith(name, testToolsImage, "")
}

// reconcileAgentWithWorkspace runs one reconcile for the named Agent, with this
// operator running agents' workspace from testWorkspaceImage.
func reconcileAgentWithWorkspace(name string) (reconcile.Result, error) {
	return reconcileAgentWith(name, "", testWorkspaceImage)
}

// reconcileAgentWith runs one reconcile for the named Agent, with the images
// this operator's own configuration carries.
func reconcileAgentWith(name, toolsImage, workspaceImage string) (reconcile.Result, error) {
	reconciler := &AgentReconciler{
		Client:         k8sClient,
		Scheme:         k8sClient.Scheme(),
		CopyImage:      testCopyImage,
		ToolsImage:     toolsImage,
		WorkspaceImage: workspaceImage,
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
