package controller

import (
	"maps"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// The revisions the specs stand in for the StatefulSet controller with: the one
// the StatefulSet is at, and one an adopted Pod was made from.
const (
	updateRevisionSuffix = "-5b5b99647"
	oldRevisionSuffix    = "-66c5b9676b"
)

// runningOnRevision builds an Agent's workload and does what the StatefulSet
// controller and a kubelet would: it records the StatefulSet's update revision
// as observed, and starts the Pod on revision, owned by that StatefulSet where
// owned is true. envtest runs neither, so the spec stands in for both.
func runningOnRevision(name, revision string, owned bool) *corev1.Pod {
	GinkgoHelper()

	createSecret(credentialsSecretName(name))
	createAgent(newAgent(name))
	_, err := reconcileAgent(name)
	Expect(err).NotTo(HaveOccurred())

	statefulSet := statefulSetFor(name)
	statefulSet.Status.ObservedGeneration = statefulSet.Generation
	statefulSet.Status.Replicas = 1
	statefulSet.Status.ReadyReplicas = 1
	statefulSet.Status.UpdateRevision = name + updateRevisionSuffix
	statefulSet.Status.CurrentRevision = name + updateRevisionSuffix
	Expect(k8sClient.Status().Update(ctx, statefulSet)).To(Succeed())

	pod := podOf(statefulSet, agentNamespace)
	pod.Labels = map[string]string{appsv1.ControllerRevisionHashLabelKey: name + revision}
	maps.Copy(pod.Labels, statefulSet.Spec.Template.Labels)
	pod.Finalizers = statefulSet.Spec.Template.Finalizers
	// Bound, as a running Pod is: the API server deletes an unbound one at once,
	// whatever grace the delete asked for.
	pod.Spec.NodeName = "roll-node"
	if owned {
		pod.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "apps/v1", Kind: "StatefulSet", Name: statefulSet.Name, UID: statefulSet.UID,
			Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
		}}
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() {
		releaseFinalizers(pod)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed())
	})

	return pod
}

var _ = Describe("Adopted Pod roll", func() {
	// #340: a StatefulSet that replaced a shared-shape one adopted its Pod and
	// recorded the rollout complete, with the Pod on the old revision.
	It("deletes a Pod on another revision than its StatefulSet's through the writer fence, and reports it rolling", func() {
		By("the control: a Pod on the StatefulSet's update revision is left running, and reported reconciled")
		current := "roll-current"
		runningOnRevision(current, updateRevisionSuffix, true)
		_, err := reconcileAgent(current)
		Expect(err).NotTo(HaveOccurred())
		pod, exists := podFor(current)
		Expect(exists).To(BeTrue())
		Expect(pod.DeletionTimestamp).To(BeNil())
		expectCondition(current, agentv1alpha1.ConditionSynced, metav1.ConditionTrue, agentv1alpha1.ReasonWorkloadReconciled)
		expectCondition(current, agentv1alpha1.ConditionAvailable, metav1.ConditionTrue, agentv1alpha1.ReasonReplicaReady)

		By("a Pod the StatefulSet adopted on the old revision")
		outdated := "roll-outdated"
		adopted := runningOnRevision(outdated, oldRevisionSuffix, true)
		_, err = reconcileAgent(outdated)
		Expect(err).NotTo(HaveOccurred())

		By("deleted, not forced, and held by the writer fence's finalizer")
		pod, exists = podFor(outdated)
		Expect(exists).To(BeTrue(), "the fence did not hold the Pod")
		Expect(pod.UID).To(Equal(adopted.UID))
		Expect(pod.DeletionTimestamp).NotTo(BeNil(), "the Pod on the old revision was not deleted")
		Expect(pod.DeletionGracePeriodSeconds).NotTo(Equal(ptr.To(int64(0))), "the Pod was force-deleted")
		Expect(controllerutil.ContainsFinalizer(pod, writerStoppedFinalizer)).To(BeTrue())

		By("reporting the workload rolling, neither reconciled nor available")
		expectCondition(outdated, agentv1alpha1.ConditionSynced, metav1.ConditionFalse, agentv1alpha1.ReasonWorkloadRolling)
		Expect(conditionOf(outdated, agentv1alpha1.ConditionSynced).Message).
			To(ContainSubstring(outdated + oldRevisionSuffix))
		expectCondition(outdated, agentv1alpha1.ConditionAvailable, metav1.ConditionFalse, agentv1alpha1.ReasonReplicaOutdated)
	})

	// #344 review: a replacing StatefulSet's status is unwritten until its
	// controller first observes it, and the Pod beside it read as reconciled.
	It("reports a Pod's rollout not observed, never reconciled, while its StatefulSet's status is not current", func() {
		name := "roll-unobserved"
		adopted := runningOnRevision(name, oldRevisionSuffix, true)
		setStatus := func(observed int64, update string) {
			GinkgoHelper()
			statefulSet := statefulSetFor(name)
			statefulSet.Status.ObservedGeneration = observed
			statefulSet.Status.UpdateRevision = update
			statefulSet.Status.CurrentRevision = update
			Expect(k8sClient.Status().Update(ctx, statefulSet)).To(Succeed())
		}
		expectNotObserved := func() {
			GinkgoHelper()
			_, err := reconcileAgent(name)
			Expect(err).NotTo(HaveOccurred())
			expectCondition(name, agentv1alpha1.ConditionSynced, metav1.ConditionFalse, agentv1alpha1.ReasonRolloutNotObserved)
			Expect(conditionOf(name, agentv1alpha1.ConditionSynced).Message).To(ContainSubstring("not observed"))
			expectCondition(name, agentv1alpha1.ConditionAvailable, metav1.ConditionUnknown,
				agentv1alpha1.ReasonRolloutNotObserved)
			pod, exists := podFor(name)
			Expect(exists).To(BeTrue())
			Expect(pod.UID).To(Equal(adopted.UID))
			Expect(pod.DeletionTimestamp).To(BeNil(), "a Pod was deleted on a revision not yet known")
		}
		generation := statefulSetFor(name).Generation

		By("a status that observed an earlier generation")
		setStatus(generation-1, name+updateRevisionSuffix)
		expectNotObserved()

		By("the control: the same Pod, on the revision a current status names, is reconciled")
		setStatus(generation, name+oldRevisionSuffix)
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		expectCondition(name, agentv1alpha1.ConditionSynced, metav1.ConditionTrue, agentv1alpha1.ReasonWorkloadReconciled)

		By("a status at the generation that names no update revision")
		setStatus(generation, "")
		expectNotObserved()
	})

	It("leaves a Pod on another revision its StatefulSet has not adopted yet, and reports it rolling", func() {
		name := "roll-unadopted"
		runningOnRevision(name, oldRevisionSuffix, false)
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod, exists := podFor(name)
		Expect(exists).To(BeTrue())
		Expect(pod.DeletionTimestamp).To(BeNil(), "a Pod the StatefulSet does not own was deleted")
		expectCondition(name, agentv1alpha1.ConditionSynced, metav1.ConditionFalse, agentv1alpha1.ReasonWorkloadRolling)
		Expect(conditionOf(name, agentv1alpha1.ConditionSynced).Message).To(ContainSubstring("not adopted"))
	})
})
