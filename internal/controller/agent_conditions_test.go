package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// retiredStateIsolated is the condition ADR 0068 removed, as an Agent an
// earlier operator reconciled still carries it.
var retiredStateIsolated = metav1.Condition{
	Type: "StateIsolated", Status: metav1.ConditionFalse, Reason: "SharedClaim",
	Message: "The workspace shares the state claim", LastTransitionTime: metav1.Now(),
}

var _ = Describe("Condition types", func() {
	// #341: the lab's Agent kept StateIsolated=False SharedClaim after ADR 0068
	// removed it, contradicting a workload whose claims were separate.
	It("removes a condition type this operator does not write after one reconcile, and leaves the ones it writes", func() {
		name := "retired-condition"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		written := readAgent(name).Status.Conditions
		Expect(written).NotTo(BeEmpty())

		By("an earlier operator's StateIsolated beside the conditions this one wrote")
		agent := readAgent(name)
		held := agent.DeepCopy()
		meta.SetStatusCondition(&agent.Status.Conditions, retiredStateIsolated)
		Expect(k8sClient.Status().Patch(ctx, agent, client.MergeFrom(held))).To(Succeed())
		Expect(meta.FindStatusCondition(readAgent(name).Status.Conditions, retiredStateIsolated.Type)).
			NotTo(BeNil(), "the setup did not store the retired condition")

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		after := readAgent(name).Status.Conditions
		Expect(meta.FindStatusCondition(after, retiredStateIsolated.Type)).To(BeNil(), "StateIsolated was kept")
		By("the control: every condition this operator wrote is kept as it was")
		Expect(after).To(Equal(written))
	})

	It("lists every condition type the reconciler writes", func() {
		name := "written-conditions"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		for _, condition := range readAgent(name).Status.Conditions {
			Expect(conditionTypes).To(ContainElement(condition.Type))
		}
	})
})
