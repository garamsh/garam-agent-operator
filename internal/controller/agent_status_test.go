package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// reportedGRN is what the poller reports on an Agent this operator constructed.
// The reconciler writes no part of it: the spec below is about the two writes
// meeting on one object rather than about the value.
const reportedGRN = "grn:garam:default:agent:95f1823fe036d0f4"

// syncedCondition is the Synced condition an Agent of that name carries, and
// fails the spec when it carries none.
func syncedCondition(name string) *metav1.Condition {
	GinkgoHelper()

	agent := readAgent(name)
	synced := meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionSynced)
	Expect(synced).NotTo(BeNil())

	return synced
}

// availableCondition is the Available condition an Agent of that name carries,
// and fails the spec when it carries none.
func availableCondition(name string) *metav1.Condition {
	GinkgoHelper()

	agent := readAgent(name)
	available := meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionAvailable)
	Expect(available).NotTo(BeNil())

	return available
}

var _ = Describe("Agent status", func() {
	It("reports the workload it reconciled, and no condition it does not set", func() {
		name := "reports-its-workload"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		agent := readAgent(name)
		Expect(agent.Status.Conditions).To(HaveLen(2))
		Expect(agent.Status.ObservedGeneration).To(Equal(agent.Generation))

		synced := syncedCondition(name)
		Expect(synced.Status).To(Equal(metav1.ConditionTrue))
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))
		Expect(synced.ObservedGeneration).To(Equal(agent.Generation))

		Expect(availableCondition(name).ObservedGeneration).To(Equal(agent.Generation))
	})

	It("reports a credentials Secret that does not exist, and stops reporting it once it does", func() {
		name := "reports-missing-credentials"
		createAgent(newAgent(name))

		By("reconciling while the Secret is absent")
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		absent := syncedCondition(name)
		Expect(absent.Status).To(Equal(metav1.ConditionFalse))
		Expect(absent.Reason).To(Equal(agentv1alpha1.ReasonCredentialsSecretMissing))
		Expect(absent.Message).To(ContainSubstring(credentialsSecretName(name)))

		// Unknown rather than False: nothing was read, which is not the same
		// as having read a workload that is not running.
		unobserved := availableCondition(name)
		Expect(unobserved.Status).To(Equal(metav1.ConditionUnknown))
		Expect(unobserved.Reason).To(Equal(agentv1alpha1.ReasonWorkloadNotObserved))

		By("reconciling once the Secret exists")
		createSecret(credentialsSecretName(name))
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		arrived := syncedCondition(name)
		Expect(arrived.Status).To(Equal(metav1.ConditionTrue))
		Expect(arrived.Reason).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))

		observed := availableCondition(name)
		Expect(observed.Status).To(Equal(metav1.ConditionFalse))
		Expect(observed.Reason).To(Equal(agentv1alpha1.ReasonReplicaNotReady))
	})

	It("reports a storage size the workload cannot be changed to, without failing", func() {
		name := "reports-immutable-storage"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(syncedCondition(name).Status).To(Equal(metav1.ConditionTrue))

		By("asking for a size the claim template cannot take")
		edited := readAgent(name)
		edited.Spec.StorageSize = resource.MustParse("2Gi")
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		synced := syncedCondition(name)
		Expect(synced.Status).To(Equal(metav1.ConditionFalse))
		Expect(synced.Reason).To(Equal(agentv1alpha1.ReasonStorageSizeImmutable))
		Expect(synced.Message).To(ContainSubstring("2Gi"))

		// Readiness is reported on a spec this controller cannot act on as well
		// as on one it can. The condition already exists from the reconcile
		// above, so the generation it was computed from is what shows this
		// reconcile set it rather than left it standing.
		available := availableCondition(name)
		Expect(available.Status).To(Equal(metav1.ConditionFalse))
		Expect(available.ObservedGeneration).To(Equal(readAgent(name).Generation))
	})

	It("reports a workload with no ready replica, and reports one that has it", func() {
		name := "reports-workload-readiness"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		By("reconciling a workload no replica is ready on")
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		notReady := availableCondition(name)
		Expect(notReady.Status).To(Equal(metav1.ConditionFalse))
		Expect(notReady.Reason).To(Equal(agentv1alpha1.ReasonReplicaNotReady))
		Expect(notReady.Message).To(ContainSubstring(name))

		// The control, and the whole of what this layer can say. envtest runs
		// neither a kubelet nor the StatefulSet controller, so readyReplicas is
		// only ever what a spec writes: the two readings below differ in that
		// field alone. Without the second, False here is consistent with a
		// controller that reads nothing and reports False always.
		By("reporting a ready replica the way the StatefulSet controller would")
		workload := statefulSetFor(name)
		workload.Status.Replicas = 1
		workload.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, workload)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		ready := availableCondition(name)
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal(agentv1alpha1.ReasonReplicaReady))

		By("leaving Synced asserting what it asserted before")
		Expect(syncedCondition(name).Status).To(Equal(metav1.ConditionTrue))
		Expect(syncedCondition(name).Reason).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))
	})

	It("tracks the generation its status was computed from", func() {
		name := "tracks-its-generation"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		first := readAgent(name)
		Expect(first.Status.ObservedGeneration).To(Equal(first.Generation))
		reported := first.Generation

		By("editing the spec, which leaves the status behind the generation")
		first.Spec.Image = "example.com/gagent:v0.2.0"
		Expect(k8sClient.Update(ctx, first)).To(Succeed())

		edited := readAgent(name)
		Expect(edited.Generation).To(BeNumerically(">", reported))
		Expect(edited.Status.ObservedGeneration).To(Equal(reported))

		By("reconciling the edit")
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		reconciled := readAgent(name)
		Expect(reconciled.Status.ObservedGeneration).To(Equal(reconciled.Generation))
	})

	It("writes its condition when the poller reported the agent GRN after this reconcile's read", func() {
		name := "writes-beside-the-poller"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		By("reading the Agent as a reconcile does, and deciding a condition from it")
		agent := readAgent(name)
		held := agent.DeepCopy()
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonCredentialsSecretMissing,
			"the Secret this Agent names is gone")

		By("reporting the agent GRN the way the poller does, after that read")
		Expect(k8sClient.Status().Patch(ctx, readAgent(name), client.RawPatch(types.MergePatchType,
			[]byte(`{"status":{"agent":"`+reportedGRN+`"}}`)))).To(Succeed())

		By("writing the condition out of the copy read before that report")
		reconciler := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		Expect(reconciler.writeStatus(ctx, agent, held)).To(Succeed())

		By("leaving both writers' fields on the object")
		Expect(readAgent(name).Status.Agent).To(Equal(reportedGRN))
		Expect(syncedCondition(name).Reason).To(Equal(agentv1alpha1.ReasonCredentialsSecretMissing))
	})

	It("writes the status no second time when nothing it observed changed", func() {
		name := "writes-its-status-once"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		reported := readAgent(name).ResourceVersion

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(readAgent(name).ResourceVersion).To(Equal(reported))
	})
})
