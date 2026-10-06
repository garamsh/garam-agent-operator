package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
)

// recoveringGRN is the GRN of the agent the recovery spec recovers: its request Secret is named
// for it, so no other spec's agent shares it.
const recoveringGRN = "grn:acme:default:agent:7e7e7e7e7e7e7e7e"

// annotateSecret sets one annotation on a Secret, as the recoverer does.
func annotateSecret(name, key, value string) {
	GinkgoHelper()

	secret := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, secret)).To(Succeed())
	edited := secret.DeepCopy()
	if edited.Annotations == nil {
		edited.Annotations = map[string]string{}
	}
	edited.Annotations[key] = value
	Expect(k8sClient.Patch(ctx, edited, client.MergeFrom(secret))).To(Succeed())
}

var _ = Describe("Recovering an agent's credential", func() {
	It("reports a recovery request, and why its certificate was refused, and moves the Pod only for a recovered lineage", func() {
		name := "recovers-its-credential"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: recoveringGRN, Source: agentv1alpha1.DesiredSourceControl}
		createAgent(agent)
		reconcileOnce(name)

		By("the control: with no request persisted, nothing is recovering and the template carries no lineage")
		expectCondition(name, agentv1alpha1.ConditionRecovery, metav1.ConditionFalse, agentv1alpha1.ReasonNotRecovering)
		Expect(statefulSetFor(name).Spec.Template.Annotations).NotTo(HaveKey(agentname.CredentialLineageAnnotation))

		By("a persisted request, which the agent reports as recovering")
		createSecret(agentname.RecoveryRequestSecret(recoveringGRN))
		reconcileOnce(name)
		expectCondition(name, agentv1alpha1.ConditionRecovery, metav1.ConditionTrue, agentv1alpha1.ReasonRecovering)

		By("its recovered certificate refused, which the agent reports under that reason")
		annotateSecret(agentname.RecoveryRequestSecret(recoveringGRN), agentname.RecoveryRefusedAnnotation,
			agentv1alpha1.ReasonRecoveredCertificateUnverified)
		reconcileOnce(name)
		expectCondition(name, agentv1alpha1.ConditionRecovery, metav1.ConditionTrue,
			agentv1alpha1.ReasonRecoveredCertificateUnverified)

		By("a recovered credential placed under a lineage, which the Pod template carries, so the Pod moves")
		before := statefulSetFor(name).Spec.Template
		annotateSecret(credentialsSecretName(name), agentname.CredentialLineageAnnotation, "lineage-2")
		reconcileOnce(name)
		after := statefulSetFor(name).Spec.Template
		Expect(after.Annotations).To(HaveKeyWithValue(agentname.CredentialLineageAnnotation, "lineage-2"))
		after.Annotations = before.Annotations
		Expect(after).To(Equal(before), "the lineage changed more of the template than its annotation")
	})

	It("reports no recovery on an agent of no Control source", func() {
		name := "recovers-nothing"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		reconcileOnce(name)
		Expect(meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionRecovery)).To(BeNil())
	})
})
