package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// neverCreatedStatus is the agent's container as the kubelet reports one it never created: waiting,
// with no container ID, no restart and no earlier state.
func neverCreatedStatus() corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: agentContainerName, Image: "example.com/image:v1",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}
}

// reportPod writes the Pod's status as a kubelet would after it finished with the Pod: its phase,
// its conditions and its writers' statuses.
func reportPod(pod *corev1.Pod, phase corev1.PodPhase, statuses []corev1.ContainerStatus, conditions ...corev1.PodCondition) {
	GinkgoHelper()

	current := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), current)).To(Succeed())
	current.Status.Phase = phase
	current.Status.ContainerStatuses = statuses
	current.Status.InitContainerStatuses = nil
	current.Status.Conditions = conditions
	Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())
}

// createNodeTainted creates a Ready node carrying taints, and removes it when the spec ends.
func createNodeTainted(name string, taints ...corev1.Taint) {
	GinkgoHelper()

	createNode(name, corev1.ConditionTrue)
	node := &corev1.Node{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, node)).To(Succeed())
	node.Spec.Taints = taints
	Expect(k8sClient.Update(ctx, node)).To(Succeed())
}

// deletedNeverCreated starts the Agent's Pod on node, deletes it, and has the kubelet report its
// agent container never created in a Pod of phase, with conditions; then reconciles the fence.
func deletedNeverCreated(name, node string, phase corev1.PodPhase, status corev1.ContainerStatus,
	conditions ...corev1.PodCondition) *corev1.Pod {
	GinkgoHelper()

	pod := fencedAgent(name, node, runningState)
	deletePod(pod)
	reportPod(pod, phase, []corev1.ContainerStatus{status}, conditions...)
	_, err := reconcileAgent(name)
	Expect(err).NotTo(HaveOccurred())

	return pod
}

// expectHeldSaying is expectHeld on a waiting writer, with the condition's message naming why.
func expectHeldSaying(agent, says string) {
	GinkgoHelper()

	expectHeld(agent, agentv1alpha1.ReasonContainerWaiting)
	fence := meta.FindStatusCondition(readAgent(agent).Status.Conditions, agentv1alpha1.ConditionWriterFence)
	Expect(fence.Message).To(ContainSubstring(says))
}

var _ = Describe("Writer fence on writers never created (ADR 0061)", func() {
	It("releases a deleted Pod the kubelet made terminal whose writer was never created, recording the phase", func() {
		createNode("fence-node-never-created", corev1.ConditionTrue)

		pod := deletedNeverCreated("fence-never-created", "fence-node-never-created", corev1.PodFailed,
			neverCreatedStatus())
		expectReleased("fence-never-created", string(pod.UID))
		evidence := readAgent("fence-never-created").Status.WriterStopped
		Expect(evidence.NeverCreated).To(ConsistOf(agentContainerName))
		Expect(evidence.PodPhase).To(Equal(corev1.PodFailed))
		Expect(evidence.Containers).To(BeEmpty())

		By("the kubelet's own DisruptionTarget, which it sets only once the Pod is terminal")
		byKubelet := deletedNeverCreated("fence-never-created-kubelet", "fence-node-never-created", corev1.PodFailed,
			neverCreatedStatus(), corev1.PodCondition{Type: corev1.DisruptionTarget,
				Status: corev1.ConditionTrue, Reason: corev1.PodReasonTerminationByKubelet})
		expectReleased("fence-never-created-kubelet", string(byKubelet.UID))
	})

	It("holds a never-created writer in a Pod the kubelet has not made terminal", func() {
		createNode("fence-node-not-terminal", corev1.ConditionTrue)

		deletedNeverCreated("fence-never-created-pending", "fence-node-not-terminal", corev1.PodPending,
			neverCreatedStatus())
		expectHeldSaying("fence-never-created-pending", `the Pod's phase is "Pending"`)
	})

	It("holds a writer that has a container ID, a restart or an earlier state, in a terminal Pod", func() {
		createNode("fence-node-had-a-container", corev1.ConditionTrue)

		withID := neverCreatedStatus()
		withID.ContainerID = "containerd://created"
		deletedNeverCreated("fence-waiting-with-id", "fence-node-had-a-container", corev1.PodFailed, withID)
		expectHeld("fence-waiting-with-id", agentv1alpha1.ReasonContainerWaiting)

		restarted := neverCreatedStatus()
		restarted.RestartCount = 1
		deletedNeverCreated("fence-waiting-restarted", "fence-node-had-a-container", corev1.PodFailed, restarted)
		expectHeld("fence-waiting-restarted", agentv1alpha1.ReasonContainerWaiting)

		earlier := neverCreatedStatus()
		earlier.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "ContainerStatusUnknown", ExitCode: 137}}
		deletedNeverCreated("fence-waiting-earlier-state", "fence-node-had-a-container", corev1.PodFailed, earlier)
		expectHeld("fence-waiting-earlier-state", agentv1alpha1.ReasonContainerWaiting)
	})

	It("holds a never-created writer whose node is not Ready, or is out of service", func() {
		createNode("fence-node-not-ready", corev1.ConditionFalse)
		deletedNeverCreated("fence-never-created-not-ready", "fence-node-not-ready", corev1.PodFailed,
			neverCreatedStatus())
		expectHeldSaying("fence-never-created-not-ready", "is not Ready")

		createNodeTainted("fence-node-out-of-service",
			corev1.Taint{Key: corev1.TaintNodeOutOfService, Value: "nodeshutdown", Effect: corev1.TaintEffectNoExecute})
		deletedNeverCreated("fence-never-created-out-of-service", "fence-node-out-of-service", corev1.PodFailed,
			neverCreatedStatus())
		expectHeldSaying("fence-never-created-out-of-service", corev1.TaintNodeOutOfService)
	})

	It("holds a never-created writer in a Pod carrying a DisruptionTarget the kubelet did not set", func() {
		createNode("fence-node-disrupted", corev1.ConditionTrue)

		for name, reason := range map[string]string{
			"fence-never-created-podgc":    "DeletionByPodGC",
			"fence-never-created-taint":    "DeletionByTaintManager",
			"fence-never-created-eviction": "EvictionByEvictionAPI",
			"fence-never-created-preempt":  corev1.PodReasonPreemptionByScheduler,
		} {
			deletedNeverCreated(name, "fence-node-disrupted", corev1.PodFailed, neverCreatedStatus(),
				corev1.PodCondition{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: reason,
					LastTransitionTime: metav1.Now()})
			expectHeldSaying(name, reason)
		}
	})
})
