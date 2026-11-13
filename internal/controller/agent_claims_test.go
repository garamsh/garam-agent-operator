package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// sharedShape replaces the Agent's StatefulSet with the one this operator built
// before the workspace had a claim of its own: one claim template, state, which
// the workspace's container mounts too (ADR 0023). The Agent still owns it.
func sharedShape(name string) {
	GinkgoHelper()

	built := statefulSetFor(name)
	// envtest runs no garbage collector, so nothing waits on this delete.
	Expect(k8sClient.Delete(ctx, built)).To(Succeed())
	Eventually(func() error {
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(built), &appsv1.StatefulSet{})
	}).Should(MatchError(apierrors.IsNotFound, "a not-found error"))

	shared := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: built.Name, Namespace: built.Namespace,
			Labels: built.Labels, OwnerReferences: built.OwnerReferences,
		},
		Spec: *built.Spec.DeepCopy(),
	}
	shared.Spec.VolumeClaimTemplates = shared.Spec.VolumeClaimTemplates[:1]
	Expect(shared.Spec.VolumeClaimTemplates[0].Name).To(Equal(stateVolumeName))
	for i := range shared.Spec.Template.Spec.Containers {
		if shared.Spec.Template.Spec.Containers[i].Name == workspaceContainerName {
			shared.Spec.Template.Spec.Containers[i].VolumeMounts = []corev1.VolumeMount{
				{Name: stateVolumeName, MountPath: agentTypeSherlock.stateMountPath},
			}
		}
	}
	Expect(k8sClient.Create(ctx, shared)).To(Succeed())

	By("reading back the shared shape, which is what leaves the replacement something to replace")
	read := statefulSetFor(name)
	Expect(read.Spec.VolumeClaimTemplates).To(HaveLen(1))
	Expect(containerOf(read.Spec.Template.Spec, workspaceContainerName).VolumeMounts).
		To(ConsistOf(HaveField("Name", stateVolumeName)))
}

// workspaceClaimOf creates the claim the StatefulSet would make for the Agent's
// workspace volume, and removes it when the spec ends.
func workspaceClaimOf(agent string) {
	GinkgoHelper()

	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: workspaceVolumeName + "-" + agent + "-0", Namespace: agentNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	Expect(k8sClient.Create(ctx, claim)).To(Succeed())
	DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, claim))).To(Succeed()) })
}

// expectClaimKept asserts that the state claim is the one created, not deleting.
func expectClaimKept(claim *corev1.PersistentVolumeClaim) {
	GinkgoHelper()

	read := &corev1.PersistentVolumeClaim{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), read)).To(Succeed())
	Expect(read.UID).To(Equal(claim.UID))
	Expect(read.DeletionTimestamp).To(BeNil())
}

// syncedReason is the reason of the Agent's Synced condition.
func syncedReason(name string) string {
	GinkgoHelper()

	synced := meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionSynced)
	Expect(synced).NotTo(BeNil())

	return synced.Reason
}

// finishOrphaning does what the garbage collector does once it has orphaned a
// StatefulSet's dependents: it takes the orphan finalizer off, and the
// StatefulSet goes. envtest runs no garbage collector.
func finishOrphaning(name string) {
	GinkgoHelper()

	releaseFinalizers(statefulSetFor(name))
	Eventually(func() error {
		return k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, &appsv1.StatefulSet{})
	}).Should(MatchError(apierrors.IsNotFound, "a not-found error"))
}

// replaceSharedShapeOf runs the reconciles that replace an Agent's shared-shape
// StatefulSet: the first deletes it leaving its dependents, and the second, once
// it is gone, creates the next. Each reconcile is a reconciler built afresh, so
// the second is what a manager restarted between the two does.
func replaceSharedShapeOf(name string) {
	GinkgoHelper()

	_, err := reconcileAgentWithWorkspace(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))
	finishOrphaning(name)

	_, err = reconcileAgentWithWorkspace(name)
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("Agent claims", func() {
	It("gives the agent's state and its workspace a claim each, each mounted into its own container", func() {
		name := "claims-apart"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		workload := statefulSetFor(name)
		pod := workload.Spec.Template.Spec

		By("claiming state under the name it always had, and a workspace beside it")
		Expect(workload.Spec.VolumeClaimTemplates).To(HaveLen(2))
		Expect(claimTemplate(workload, stateVolumeName)).NotTo(BeNil())
		Expect(claimTemplate(workload, workspaceVolumeName)).NotTo(BeNil())

		By("the control: the agent mounts the state claim where its memory path points")
		Expect(containerOf(pod, agentContainerName).VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: stateVolumeName, MountPath: agentTypeSherlock.stateMountPath}))
		Expect(containerOf(pod, agentContainerName).VolumeMounts).
			NotTo(ContainElement(HaveField("Name", workspaceVolumeName)))

		By("the workspace mounting its own claim at that path, and no container but the agent mounting state")
		Expect(containerOf(pod, workspaceContainerName).VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: workspaceVolumeName, MountPath: agentTypeSherlock.stateMountPath}))
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if container.Name != agentContainerName {
				Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", stateVolumeName)), container.Name)
			}
		}
	})

	It("refuses a workspace size of zero at admission, beside one greater than zero", func() {
		By("the control: a workspace size greater than zero is admitted")
		admitted := newAgent("workspace-size-admitted")
		admitted.Spec.WorkspaceStorageSize = ptr.To(resource.MustParse("1Mi"))
		Expect(k8sClient.Create(ctx, admitted)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, admitted))).To(Succeed()) })

		refused := newAgent("workspace-size-zero")
		refused.Spec.WorkspaceStorageSize = ptr.To(resource.MustParse("0"))
		Expect(k8sClient.Create(ctx, refused)).
			To(MatchError(ContainSubstring("workspaceStorageSize must be greater than zero")))
	})

	It("sizes the workspace's claim at the Agent's workspace size, and at its state's where it names none", func() {
		By("the control: an Agent naming no workspace size")
		unnamed := "workspace-size-unset"
		createSecret(credentialsSecretName(unnamed))
		createAgent(newAgent(unnamed))
		_, err := reconcileAgent(unnamed)
		Expect(err).NotTo(HaveOccurred())
		defaulted := statefulSetFor(unnamed)
		Expect(claimedStorageSize(defaulted, workspaceVolumeName)).To(BeComparableTo(resource.MustParse("1Gi")))

		By("an Agent naming one")
		named := "workspace-size-set"
		createSecret(credentialsSecretName(named))
		agent := newAgent(named)
		agent.Spec.WorkspaceStorageSize = ptr.To(resource.MustParse("3Gi"))
		createAgent(agent)
		_, err = reconcileAgent(named)
		Expect(err).NotTo(HaveOccurred())
		sized := statefulSetFor(named)
		Expect(claimedStorageSize(sized, workspaceVolumeName)).To(BeComparableTo(resource.MustParse("3Gi")))
		Expect(claimedStorageSize(sized, stateVolumeName)).To(BeComparableTo(resource.MustParse("1Gi")))
		Expect(claimTemplate(sized, workspaceVolumeName).Spec.StorageClassName).
			To(Equal(claimTemplate(sized, stateVolumeName).Spec.StorageClassName))

		By("reporting a workspace size the claim cannot follow, as the state's size is")
		changed := readAgent(named)
		changed.Spec.WorkspaceStorageSize = ptr.To(resource.MustParse("5Gi"))
		Expect(k8sClient.Update(ctx, changed)).To(Succeed())
		_, err = reconcileAgent(named)
		Expect(err).NotTo(HaveOccurred())
		Expect(syncedReason(named)).To(Equal(agentv1alpha1.ReasonStorageSizeImmutable))
		Expect(syncedReason(unnamed)).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))
	})

	It("replaces a StatefulSet whose workspace shares the state claim, keeping that claim by its UID and copying nothing", func() {
		name := "upgrades-its-claims"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		sharedShape(name)
		claim := createClaim(name)
		old := statefulSetFor(name)

		By("reconciling, which deletes the shared shape and leaves its Pod and claims to the garbage collector to orphan")
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		deleting := statefulSetFor(name)
		Expect(deleting.UID).To(Equal(old.UID))
		Expect(deleting.DeletionTimestamp).NotTo(BeNil())
		Expect(deleting.Finalizers).To(ContainElement(metav1.FinalizerOrphanDependents))
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))
		expectClaimKept(claim)

		By("reconciling while it is still deleting, which creates nothing")
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).UID).To(Equal(old.UID))
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))

		By("creating the next once it is gone, with a reconciler built afresh as a restarted manager's would be")
		finishOrphaning(name)
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		next := statefulSetFor(name)
		Expect(next.UID).NotTo(Equal(old.UID))
		Expect(next.Spec.VolumeClaimTemplates).To(HaveLen(2))
		Expect(claimTemplate(next, stateVolumeName)).NotTo(BeNil())
		expectClaimKept(claim)

		By("the next StatefulSet's state template naming the kept claim, so the Pod mounts that claim by its name")
		Expect(stateVolumeName + "-" + agentPodName(readAgent(name))).To(Equal(claim.Name))

		By("the state claim mounted by the agent alone, and no container copying it onto the workspace's new claim")
		pod := next.Spec.Template.Spec
		Expect(claimTemplate(next, workspaceVolumeName)).NotTo(BeNil())
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if container.Name != agentContainerName {
				Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", stateVolumeName)), container.Name)
			}
		}

		By("reconciling again, which keeps the StatefulSet")
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		kept := statefulSetFor(name)
		Expect(kept.UID).To(Equal(next.UID))
		Expect(kept.DeletionTimestamp).To(BeNil())
		expectClaimKept(claim)
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))
	})

	It("replaces a shared-shape StatefulSet with no live Pod, as a suspended agent leaves it", func() {
		name := "migrates-stopped"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		sharedShape(name)
		claim := createClaim(name)
		_, exists := podFor(name)
		Expect(exists).To(BeFalse(), "no Pod runs, which is the stop this spec stands for")

		By("reporting the replacement while it runs")
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))
		finishOrphaning(name)

		By("creating the separate shape on the same state claim, and no Pod of the operator's own")
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		next := statefulSetFor(name)
		Expect(claimTemplate(next, workspaceVolumeName)).NotTo(BeNil())
		expectClaimKept(claim)
		_, exists = podFor(name)
		Expect(exists).To(BeFalse())
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReconciled))
	})

	It("holds the shared shape's Pod across the replacement until its writers stop, on the claim it started on", func() {
		name := "fenced-across-replacement"
		node := "node-" + name
		createNode(node, corev1.ConditionTrue)
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		sharedShape(name)
		claim := createClaim(name)
		pod := startPod(name, node, map[string]corev1.ContainerState{
			agentContainerName: runningState, workspaceContainerName: runningState,
		})

		By("replacing the StatefulSet, while the old Pod runs and records the claim it started on")
		replaceSharedShapeOf(name)
		running, exists := podFor(name)
		Expect(exists).To(BeTrue())
		Expect(running.UID).To(Equal(pod.UID))
		Expect(running.Annotations).To(HaveKeyWithValue(pvcUIDAnnotation, string(claim.UID)))

		By("the new StatefulSet rolling the Pod it adopted, which deletes it while both writers run")
		deletePod(pod)
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		expectHeld(name, agentv1alpha1.ReasonContainerRunning)
		held, _ := podFor(name)
		Expect(held.UID).To(Equal(pod.UID), "the next Pod's name is still taken")

		By("the control: its writers stopped, it is released on evidence naming the claim it started on")
		reportContainers(pod, map[string]corev1.ContainerState{
			agentContainerName: terminatedState, workspaceContainerName: terminatedState,
		})
		_, err = reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))
		Expect(readAgent(name).Status.WriterStopped.PVCUID).To(Equal(string(claim.UID)))
		expectClaimKept(claim)
	})
})
