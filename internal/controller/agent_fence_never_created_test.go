package controller

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
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

// unlocatedState is what the kubelet's TerminatePod writes for a container it could not locate: terminated,
// with no container ID (kubernetes v1.37.0 pkg/kubelet/status/status_manager.go:747-753, 763-769).
var unlocatedState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
	Reason: "ContainerStatusUnknown", Message: "The container could not be located when the pod was terminated",
	ExitCode: 137,
}}

// terminateUncreated writes the status the kubelet leaves on a deleted Pod none of whose containers was
// created, as kubernetes v1.37.0 TerminatePod makes it (pkg/kubelet/status/status_manager.go:721-848): phase
// Failed; the first init container unlocated and the rest waiting; every regular container unlocated where the
// Pod has no init container, since hasPodInitialized then holds, and waiting where it has one.
func terminateUncreated(pod *corev1.Pod) *corev1.Pod {
	GinkgoHelper()

	current := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), current)).To(Succeed())
	waiting := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}
	regular := waiting
	if len(current.Spec.InitContainers) == 0 {
		regular = unlocatedState
	}
	current.Status.Phase = corev1.PodFailed
	current.Status.Conditions = nil
	current.Status.InitContainerStatuses = nil
	for i, container := range current.Spec.InitContainers {
		state := waiting
		if i == 0 {
			state = unlocatedState
		}
		current.Status.InitContainerStatuses = append(current.Status.InitContainerStatuses,
			corev1.ContainerStatus{Name: container.Name, Image: container.Image, State: state})
	}
	current.Status.ContainerStatuses = nil
	for _, container := range current.Spec.Containers {
		current.Status.ContainerStatuses = append(current.Status.ContainerStatuses,
			corev1.ContainerStatus{Name: container.Name, Image: container.Image, State: regular})
	}
	Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())

	written := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), written)).To(Succeed())
	Expect(written.Status.Phase).To(Equal(corev1.PodFailed))
	Expect(written.Status.ContainerStatuses).To(HaveLen(len(written.Spec.Containers)))
	Expect(written.Status.InitContainerStatuses).To(HaveLen(len(written.Spec.InitContainers)))

	return written
}

var _ = Describe("Writer fence on a Pod deleted before any writer was created (#363)", func() {
	It("holds a Pod with no init container for a hand release, since the kubelet reports its writers terminated with no ID", func() {
		createNode("fence-node-uncreated", corev1.ConditionTrue)
		waiting := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}

		pod := fencedAgent("fence-uncreated-no-init", "fence-node-uncreated", waiting)
		Expect(pod.Spec.InitContainers).To(BeEmpty())
		deletePod(pod)
		written := terminateUncreated(pod)
		Expect(written.Status.ContainerStatuses).To(ConsistOf(HaveField("State", unlocatedState)))
		_, err := reconcileAgent("fence-uncreated-no-init")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-uncreated-no-init", agentv1alpha1.ReasonNoContainerStatus)
		fence := meta.FindStatusCondition(readAgent("fence-uncreated-no-init").Status.Conditions,
			agentv1alpha1.ConditionWriterFence)
		Expect(fence.Message).To(ContainSubstring("terminated instance with no container ID"))
		Expect(fence.Message).To(ContainSubstring("or by a person"))

		By("the control: the same Pod, its writer terminated with an ID, is released")
		control := fencedAgent("fence-uncreated-no-init-control", "fence-node-uncreated", waiting)
		deletePod(control)
		withID := unlocatedState.DeepCopy()
		withID.Terminated.ContainerID = "containerd://stopped"
		reportPod(control, corev1.PodFailed,
			[]corev1.ContainerStatus{{Name: agentContainerName, Image: "example.com/image:v1", State: *withID}})
		_, err = reconcileAgent("fence-uncreated-no-init-control")
		Expect(err).NotTo(HaveOccurred())
		expectReleased("fence-uncreated-no-init-control", string(control.UID))
	})

	It("releases a managed agent's Pod, whose adapter sidecar waits behind the init containers before it", func() {
		createNode("fence-node-uncreated-managed", corev1.ConditionTrue)
		name := "fence-uncreated-managed"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)
		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx,
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.PlacementSecret(name), Namespace: agentNamespace}}))).
				To(Succeed())
		})
		createClaim(name)
		pod := startPod(name, "fence-node-uncreated-managed", map[string]corev1.ContainerState{})
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())

		By("the adapter is a restartable init container, and not the first")
		initContainers := pod.Spec.InitContainers
		adapterAt := slices.IndexFunc(initContainers, func(container corev1.Container) bool {
			return container.Name == adapterContainerName
		})
		Expect(adapterAt).To(BeNumerically(">", 0))
		Expect(initContainers[adapterAt].RestartPolicy).To(HaveValue(Equal(corev1.ContainerRestartPolicyAlways)))

		deletePod(pod)
		written := terminateUncreated(pod)
		Expect(written.Status.InitContainerStatuses[adapterAt].State.Waiting).NotTo(BeNil())
		Expect(written.Status.ContainerStatuses).To(ConsistOf(HaveField("State.Waiting", Not(BeNil()))))
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))
		evidence := readAgent(name).Status.WriterStopped
		Expect(evidence.NeverCreated).To(ConsistOf(agentContainerName, adapterContainerName))
		Expect(evidence.PodPhase).To(Equal(corev1.PodFailed))
	})
})
