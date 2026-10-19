package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// renderer is the field manager the control service's renders are made under.
const renderer = "garam-operator-renderer"

// setStopped sets the Agent's spec.stopped as the renderer does from the control service's feed.
func setStopped(name string, stopped bool) {
	GinkgoHelper()

	agent := readAgent(name)
	edited := agent.DeepCopy()
	edited.Spec.Stopped = stopped
	Expect(k8sClient.Patch(ctx, edited, client.MergeFrom(agent), client.FieldOwner(renderer))).To(Succeed())
	Expect(readAgent(name).Spec.Stopped).To(Equal(stopped))
}

// controlAgent creates an Agent with an identity and its workload, the claim of its volume, and its
// Pod on node with every container in state, as fencedAgent does for an Agent with none.
func controlAgent(name, node string, state corev1.ContainerState) *corev1.Pod {
	GinkgoHelper()

	createSecret(credentialsSecretName(name))
	agent := newAgent(name)
	agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
	createAgent(agent)
	reconcileOnce(name)
	createClaim(name)
	pod := startPod(name, node, map[string]corev1.ContainerState{agentContainerName: state})
	reconcileOnce(name)

	return pod
}

var _ = Describe("Stopping an agent from the control service", func() {
	It("scales a stopped agent to no replica and keeps its Unverified Pod held, with no replacement, across reconciles", func() {
		name := "stop-unverified"
		createNode("stop-node-unknown", corev1.ConditionUnknown)
		pod := controlAgent(name, "stop-node-unknown", runningState)

		By("the control: an agent the control service does not stop runs one replica")
		Expect(statefulSetFor(name).Spec.Replicas).To(Equal(ptr.To[int32](1)))
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonNotSuspended)

		By("the stop, which asks for no replica as a suspension does")
		setStopped(name, true)
		reconcileOnce(name)
		Expect(statefulSetFor(name).Spec.Replicas).To(Equal(ptr.To[int32](0)))
		expectCondition(name, agentv1alpha1.ConditionSuspended, metav1.ConditionFalse, agentv1alpha1.ReasonSuspending)
		expectCondition(name, agentv1alpha1.ConditionAvailable, metav1.ConditionFalse, agentv1alpha1.ReasonSuspended)
		Expect(conditionOf(name, agentv1alpha1.ConditionAvailable).Message).To(ContainSubstring("spec.stopped"))

		By("its Pod deleted on a node whose state is Unknown, which the fence holds")
		deletePod(pod)
		result, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(fenceRecheckInterval))
		expectHeld(name, agentv1alpha1.ReasonNodeUnknown)

		By("every later reconcile, as after a manager restart, which keeps it held and asks for no replacement")
		for range 2 {
			reconcileOnce(name)
			expectHeld(name, agentv1alpha1.ReasonNodeUnknown)
			Expect(statefulSetFor(name).Spec.Replicas).To(Equal(ptr.To[int32](0)))
		}
		held, exists := podFor(name)
		Expect(exists).To(BeTrue())
		Expect(held.UID).To(Equal(pod.UID), "a replacement Pod was made")
	})

	It("admits spec.stopped only on an Agent with an identity", func() {
		By("the control: an Agent with an identity may be stopped")
		agent := newAgent("stopped-on-control")
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		agent.Spec.Stopped = true
		createAgent(agent)
		Expect(readAgent(agent.Name).Spec.Stopped).To(BeTrue())

		elsewhere := newAgent("stopped-elsewhere")
		elsewhere.Spec.Stopped = true
		Expect(k8sClient.Create(ctx, elsewhere)).
			To(MatchError(ContainSubstring("stopped is set only on an agent with an identity")))
	})
})
