package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// person is the field manager a person's edits to an Agent are made under.
const person = "kubectl-edit"

// setSuspended sets the Agent's spec.suspended as a person does.
func setSuspended(name string, suspended bool) {
	GinkgoHelper()

	agent := readAgent(name)
	edited := agent.DeepCopy()
	edited.Spec.Suspended = suspended
	Expect(k8sClient.Patch(ctx, edited, client.MergeFrom(agent), client.FieldOwner(person))).To(Succeed())
	Expect(readAgent(name).Spec.Suspended).To(Equal(suspended))
}

// conditionOf is the Agent's condition of type kind.
func conditionOf(name, kind string) metav1.Condition {
	GinkgoHelper()

	condition := meta.FindStatusCondition(readAgent(name).Status.Conditions, kind)
	Expect(condition).NotTo(BeNil(), "the Agent reports no %s condition", kind)

	return *condition
}

// expectCondition asserts the Agent's condition of type kind.
func expectCondition(name, kind string, status metav1.ConditionStatus, reason string) {
	GinkgoHelper()

	condition := conditionOf(name, kind)
	Expect(condition.Status).To(Equal(status), condition.Message)
	Expect(condition.Reason).To(Equal(reason), condition.Message)
}

// reconcileOnce reconciles the Agent and expects no error.
func reconcileOnce(name string) {
	GinkgoHelper()

	_, err := reconcileAgent(name)
	Expect(err).NotTo(HaveOccurred())
}

// expectPodGone waits for the Agent's Pod to be gone.
func expectPodGone(name string) {
	GinkgoHelper()

	Eventually(func() bool {
		_, exists := podFor(name)
		return exists
	}).Should(BeFalse(), "the Pod is still there")
}

// readClaim reads a claim back by name.
func readClaim(name string) *corev1.PersistentVolumeClaim {
	GinkgoHelper()

	claim := &corev1.PersistentVolumeClaim{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, claim)).To(Succeed())

	return claim
}

var _ = Describe("Suspending an agent", func() {
	It("scales a suspended agent to no replica and releases its Pod on evidence alone, keeping the Agent, its credential and both claims", func() {
		name := "suspend-releases"
		createNode("suspend-node-releases", corev1.ConditionTrue)
		pod := fencedAgent(name, "suspend-node-releases", runningState)
		workspaceClaimOf(name)
		state := readClaim(stateVolumeName + "-" + name + "-0")
		workspace := readClaim(workspaceVolumeName + "-" + name + "-0")
		credential := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: credentialsSecretName(name)}, credential)).To(Succeed())

		By("the control: an agent not suspended runs one replica, and says so")
		Expect(statefulSetFor(name).Spec.Replicas).To(Equal(ptr.To[int32](1)))
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonNotSuspended)

		By("suspending it, which asks for no replica and deletes nothing itself")
		setSuspended(name, true)
		reconcileOnce(name)
		Expect(statefulSetFor(name).Spec.Replicas).To(Equal(ptr.To[int32](0)))
		running, exists := podFor(name)
		Expect(exists).To(BeTrue())
		Expect(running.DeletionTimestamp).To(BeNil(), "the manager deleted the Pod itself")
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonSuspending)
		expectCondition(name, agentv1alpha1.ConditionAvailable, metav1.ConditionFalse, agentv1alpha1.ReasonSuspended)

		By("the StatefulSet controller deleting the Pod once its writer stopped, which the fence releases on evidence")
		reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: terminatedState})
		deletePod(pod)
		reconcileOnce(name)
		expectReleased(name, string(pod.UID))
		expectPodGone(name)

		By("reporting the agent suspended once its Pod is gone")
		reconcileOnce(name)
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionTrue, agentv1alpha1.ReasonSuspended)

		By("keeping the Agent, its credential and both claims")
		Expect(readAgent(name).DeletionTimestamp).To(BeNil())
		kept := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(credential), kept)).To(Succeed())
		Expect(kept.UID).To(Equal(credential.UID))
		expectClaimKept(state)
		expectClaimKept(workspace)
	})

	It("holds a suspended agent's Pod while its writer runs, and does not report it suspended", func() {
		createNode("suspend-node-held", corev1.ConditionTrue)

		By("the control: a suspended agent whose writer stopped is released and reported suspended")
		stopped := fencedAgent("suspend-held-control", "suspend-node-held", terminatedState)
		setSuspended("suspend-held-control", true)
		reconcileOnce("suspend-held-control")
		deletePod(stopped)
		reconcileOnce("suspend-held-control")
		expectReleased("suspend-held-control", string(stopped.UID))
		expectPodGone("suspend-held-control")
		reconcileOnce("suspend-held-control")
		expectCondition("suspend-held-control", agentv1alpha1.ConditionSuspended, metav1.ConditionTrue, agentv1alpha1.ReasonSuspended)

		By("a suspended agent whose Pod is deleted while its writer still runs")
		running := fencedAgent("suspend-held", "suspend-node-held", runningState)
		setSuspended("suspend-held", true)
		reconcileOnce("suspend-held")
		deletePod(running)
		result, err := reconcileAgent("suspend-held")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(fenceRecheckInterval))
		expectHeld("suspend-held", agentv1alpha1.ReasonContainerRunning)
		reconcileOnce("suspend-held")
		expectHeld("suspend-held", agentv1alpha1.ReasonContainerRunning)
		expectCondition("suspend-held", agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonSuspending)
	})

	It("resumes a suspended agent on the same StatefulSet, which binds the same claims", func() {
		name := "suspend-resumes"
		createNode("suspend-node-resumes", corev1.ConditionTrue)
		first := fencedAgent(name, "suspend-node-resumes", terminatedState)
		workspaceClaimOf(name)
		state := readClaim(stateVolumeName + "-" + name + "-0")
		workspace := readClaim(workspaceVolumeName + "-" + name + "-0")
		before := statefulSetFor(name)
		Expect(readAgent(name).Status.Placement).NotTo(BeNil())
		Expect(readAgent(name).Status.Placement.PVCUID).To(Equal(string(state.UID)))

		By("suspending it, and releasing its Pod on evidence")
		setSuspended(name, true)
		reconcileOnce(name)
		deletePod(first)
		reconcileOnce(name)
		expectReleased(name, string(first.UID))
		expectPodGone(name)

		By("resuming it, which asks the same StatefulSet for its replica again")
		setSuspended(name, false)
		reconcileOnce(name)
		resumed := statefulSetFor(name)
		Expect(resumed.UID).To(Equal(before.UID), "the StatefulSet was replaced")
		Expect(resumed.Spec.Replicas).To(Equal(ptr.To[int32](1)))
		Expect(resumed.Spec.VolumeClaimTemplates).To(Equal(before.Spec.VolumeClaimTemplates))
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonNotSuspended)

		By("the next Pod starting on the claims the first one had")
		second := startPod(name, "suspend-node-resumes", map[string]corev1.ContainerState{agentContainerName: runningState})
		Expect(second.UID).NotTo(Equal(first.UID))
		reconcileOnce(name)
		placement := readAgent(name).Status.Placement
		Expect(placement.PodUID).To(Equal(string(second.UID)))
		Expect(placement.PVCUID).To(Equal(string(state.UID)))
		expectClaimKept(state)
		expectClaimKept(workspace)
	})

	It("scales a suspended agent's shared-shape StatefulSet to no replica without replacing it, and replaces it once resumed", func() {
		By("the control: a shared-shape StatefulSet of an agent not suspended is replaced, as ADR 0044 has it")
		control := "suspend-shape-control"
		createSecret(credentialsSecretName(control))
		createAgent(newAgent(control))
		_, err := reconcileAgentWithWorkspace(control)
		Expect(err).NotTo(HaveOccurred())
		sharedShape(control)
		createClaim(control)
		_, err = reconcileAgentWithWorkspace(control)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(control).DeletionTimestamp).NotTo(BeNil())
		Expect(syncedReason(control)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))

		By("a suspended agent's shared-shape StatefulSet, scaled to no replica and kept")
		name := "suspend-shape"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		sharedShape(name)
		state := createClaim(name)
		old := statefulSetFor(name)
		setSuspended(name, true)
		for range 2 {
			_, err = reconcileAgentWithWorkspace(name)
			Expect(err).NotTo(HaveOccurred())
			kept := statefulSetFor(name)
			Expect(kept.UID).To(Equal(old.UID))
			Expect(kept.DeletionTimestamp).To(BeNil(), "the shared shape was replaced while suspended")
			Expect(kept.Spec.Replicas).To(Equal(ptr.To[int32](0)))
			Expect(kept.Spec.VolumeClaimTemplates).To(HaveLen(1))
			Expect(kept.Spec.Template).To(Equal(old.Spec.Template), "more than the replica count was changed")
			Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonReplacementDeferred))
		}
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionTrue, agentv1alpha1.ReasonSuspended)

		By("resuming it, which replaces the shared shape and seeds the workspace once from the state claim")
		setSuspended(name, false)
		replaceSharedShapeOf(name)
		next := statefulSetFor(name)
		Expect(next.UID).NotTo(Equal(old.UID))
		Expect(next.Spec.Replicas).To(Equal(ptr.To[int32](1)))
		Expect(next.Spec.VolumeClaimTemplates).To(HaveLen(2))
		Expect(next.Annotations).To(HaveKeyWithValue(seedAnnotation, stateVolumeName))
		Expect(next.Spec.Template.Spec.InitContainers).To(ContainElement(HaveField("Name", seedContainerName)))
		expectClaimKept(state)
	})
})
