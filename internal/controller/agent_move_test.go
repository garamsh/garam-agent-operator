package controller

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// exitedState is an agent container that exited with code.
func exitedState(code int32) corev1.ContainerState {
	return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ContainerID: "containerd://exited", ExitCode: code,
	}}
}

// stopPod stops the Agent's Pod as a kubelet would with its agent exiting code,
// and runs the reconcile that releases it on that evidence.
func stopPod(name string, pod *corev1.Pod, code int32) {
	GinkgoHelper()

	reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: exitedState(code)})
	deletePod(pod)
	reconcileOnce(name)
	Eventually(func() bool { _, exists := podFor(name); return exists }).Should(BeFalse())
}

// drainedAgent builds an Agent's workload on its own node, runs its first Pod
// and stops it with its agent exiting code, so the state claim records that
// writer. It returns the state claim.
func drainedAgent(name string, code int32) *corev1.PersistentVolumeClaim {
	GinkgoHelper()

	createNode(name+"-node", corev1.ConditionTrue)
	createSecret(credentialsSecretName(name))
	createAgent(newAgent(name))
	reconcileOnce(name)
	source := createClaim(name)
	pod := startPod(name, name+"-node", map[string]corev1.ContainerState{agentContainerName: runningState})
	reconcileOnce(name)
	stopPod(name, pod, code)

	return source
}

// setMove asks the Agent to move its memory under id.
func setMove(name, id string) {
	GinkgoHelper()

	agent := readAgent(name)
	moved := agent.DeepCopy()
	moved.Spec.MemoryMove = &agentv1alpha1.MemoryMoveSpec{ID: id, StorageSize: resource.MustParse("2Gi")}
	Expect(k8sClient.Patch(ctx, moved, client.MergeFrom(agent))).To(Succeed())
}

// claimOf reads a claim back, and reports false where it does not exist.
func claimOf(name string) (*corev1.PersistentVolumeClaim, bool) {
	GinkgoHelper()

	claim := &corev1.PersistentVolumeClaim{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, claim)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		releaseFinalizers(claim)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, claim))).To(Succeed())
	})

	return claim, true
}

// moveJobOf reads back the Job of step of the Agent's move id, and reports false
// where there is none.
func moveJobOf(name, id, step string) (*batchv1.Job, bool) {
	GinkgoHelper()

	job := &batchv1.Job{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: moveJobName(readAgent(name), id, step)}, job)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, job,
			client.PropagationPolicy(metav1.DeletePropagationBackground)))).To(Succeed())
	})

	return job, true
}

// finishJob does what the Job controller and a kubelet would for a move's Job:
// it runs its one Pod to an exit with the termination message given.
func finishJob(job *batchv1.Job, code int32, message string) {
	GinkgoHelper()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: job.Name + "-run", Namespace: job.Namespace, Labels: map[string]string{jobNameLabel: job.Name},
		},
		Spec: *job.Spec.Template.Spec.DeepCopy(),
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed())
	})
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: moveContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ContainerID: "containerd://move", ExitCode: code, Message: message,
		}},
	}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

// markReady reports the Pod and its agent's container ready, as a kubelet does
// once every container started and no probe holds it.
func markReady(pod *corev1.Pod) {
	GinkgoHelper()

	current := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), current)).To(Succeed())
	current.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	current.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: agentContainerName, Ready: true, State: runningState, Image: agentImageOf(current),
	}}
	Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())
}

// agentImageOf is the image the Pod's agent container runs.
func agentImageOf(pod *corev1.Pod) string {
	for _, container := range pod.Spec.Containers {
		if container.Name == agentContainerName {
			return container.Image
		}
	}

	return ""
}

// moveCondition is the Agent's MemoryMove condition.
func moveCondition(name string) *metav1.Condition {
	GinkgoHelper()

	condition := meta.FindStatusCondition(readAgent(name).Status.Conditions, agentv1alpha1.ConditionMemoryMove)
	Expect(condition).NotTo(BeNil())

	return condition
}

// expectStoppedForMove asserts that the Agent's workload asks for no replica.
func expectStoppedForMove(name string) {
	GinkgoHelper()

	Expect(statefulSetFor(name).Spec.Replicas).To(HaveValue(BeZero()))
}

// phaseOf is the move phase recorded on a claim.
func phaseOf(claim string) string {
	GinkgoHelper()

	read, exists := claimOf(claim)
	Expect(exists).To(BeTrue(), "claim %q does not exist", claim)

	return read.Annotations[movePhaseAnnotation]
}

// stateVolumeClaim is the claim the StatefulSet's Pod template mounts as its
// state volume by name, and empty where its own claim template makes it.
func stateVolumeClaim(name string) string {
	GinkgoHelper()

	for _, volume := range statefulSetFor(name).Spec.Template.Spec.Volumes {
		if volume.Name == stateVolumeName && volume.PersistentVolumeClaim != nil {
			return volume.PersistentVolumeClaim.ClaimName
		}
	}

	return ""
}

// copyAndVerify runs the move id of a stopped agent through its copy and its
// verification, both reporting digest, up to the reconcile that switches it.
func copyAndVerify(name, id, digest string) {
	GinkgoHelper()

	setMove(name, id)
	reconcileOnce(name)
	Expect(phaseOf(targetClaimName(readAgent(name), id))).To(Equal(movePhaseCreated))
	reconcileOnce(name)
	copyJob, exists := moveJobOf(name, id, "copy")
	Expect(exists).To(BeTrue(), "no copy Job was created")
	finishJob(copyJob, 0, "source="+digest)
	reconcileOnce(name)
	reconcileOnce(name)
	verifyJob, exists := moveJobOf(name, id, "verify")
	Expect(exists).To(BeTrue(), "no verification Job was created")
	finishJob(verifyJob, 0, "source="+digest+" target="+digest+" extra=0")
	reconcileOnce(name)
	Expect(phaseOf(targetClaimName(readAgent(name), id))).To(Equal(movePhaseVerified))
}

var _ = Describe("Memory move", func() {
	It("moves the memory through a copy, a verification and a switch, and the agent accepts it before it is settled", func() {
		name := "move-whole"
		source := drainedAgent(name, 0)
		target := targetClaimName(readAgent(name), "first")

		By("stopping the agent and creating the target only once its writer is seen to exit 0")
		setMove(name, "first")
		reconcileOnce(name)
		expectStoppedForMove(name)
		created, exists := claimOf(target)
		Expect(exists).To(BeTrue())
		Expect(created.Annotations).To(HaveKeyWithValue(moveSourceAnnotation, source.Name))
		Expect(created.Annotations).To(HaveKeyWithValue(moveSourceUIDAnnotation, string(source.UID)))
		Expect(created.OwnerReferences).To(BeEmpty(), "the memory's claim must outlive the Agent")
		Expect(*created.Spec.Resources.Requests.Storage()).To(Equal(resource.MustParse("2Gi")))
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonMoveCopying))

		By("copying with the copy image, as the agent's user, reading the source and writing the target, once")
		reconcileOnce(name)
		reconcileOnce(name)
		copyJob, exists := moveJobOf(name, "first", "copy")
		Expect(exists).To(BeTrue())
		Expect(copyJob.Spec.BackoffLimit).To(HaveValue(BeZero()))
		pod := copyJob.Spec.Template.Spec
		Expect(pod.AutomountServiceAccountToken).To(HaveValue(BeFalse()))
		Expect(pod.SecurityContext.RunAsUser).To(HaveValue(Equal(int64(agentRunAsUser))))
		Expect(pod.Containers[0].Image).To(Equal(testCopyImage))
		Expect(pod.Containers[0].VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: moveSourceVolume, MountPath: moveSourceMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: moveTargetVolume, MountPath: moveTargetMountPath},
		))
		Expect(pod.Volumes).To(ContainElements(
			claimVolume(moveSourceVolume, source.Name, true), claimVolume(moveTargetVolume, target, false)))
		finishJob(copyJob, 0, "source=abc")
		reconcileOnce(name)
		Expect(phaseOf(target)).To(Equal(movePhaseCopied))

		By("verifying with both claims read-only")
		reconcileOnce(name)
		verifyJob, exists := moveJobOf(name, "first", "verify")
		Expect(exists).To(BeTrue())
		Expect(verifyJob.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveEach(HaveField("ReadOnly", BeTrue())))
		finishJob(verifyJob, 0, "source=abc target=abc extra=0")
		reconcileOnce(name)
		Expect(phaseOf(target)).To(Equal(movePhaseVerified))
		Expect(containerOf(statefulSetFor(name).Spec.Template.Spec, agentContainerName).Args).
			NotTo(ContainElement(sherlockRequireStoreFlag), "the flag is rendered only once the copy is in use")

		By("switching: the StatefulSet whose template makes the source claim is replaced, keeping its claims")
		reconcileOnce(name)
		linked, _ := claimOf(source.Name)
		Expect(linked.Annotations).To(HaveKeyWithValue(movedToAnnotation, target))
		Expect(syncedReason(name)).To(Equal(agentv1alpha1.ReasonWorkloadReplacing))
		finishOrphaning(name)
		reconcileOnce(name)
		statefulSet := statefulSetFor(name)
		Expect(claimTemplate(statefulSet, stateVolumeName)).To(BeNil())
		Expect(stateVolumeClaim(name)).To(Equal(target))
		Expect(statefulSet.Spec.Replicas).To(HaveValue(BeEquivalentTo(1)))
		Expect(containerOf(statefulSet.Spec.Template.Spec, agentContainerName).Args).To(ContainElement(sherlockRequireStoreFlag))
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonMoveStarting))

		By("settling the move only once the first Pod on the copy serves")
		started := startPod(name, name+"-node", map[string]corev1.ContainerState{agentContainerName: runningState})
		reconcileOnce(name)
		Expect(phaseOf(target)).To(Equal(movePhaseSwitched))
		markReady(started)
		reconcileOnce(name)
		Expect(phaseOf(target)).To(Equal(movePhaseAccepted))
		Expect(moveCondition(name).Status).To(Equal(metav1.ConditionTrue))
		kept, exists := claimOf(source.Name)
		Expect(exists).To(BeTrue())
		Expect(kept.DeletionTimestamp).To(BeNil(), "the source claim is never deleted by the move")

		By("fencing the next stop on the claim the memory moved to")
		stopPod(name, started, 0)
		Expect(readAgent(name).Status.WriterStopped.PVCUID).To(Equal(string(created.UID)))
		moved, _ := claimOf(target)
		var writer lastWriter
		Expect(json.Unmarshal([]byte(moved.Annotations[lastWriterAnnotation]), &writer)).To(Succeed())
		Expect(writer).To(Equal(lastWriter{PodUID: string(started.UID), State: writerStopped, ExitCode: ptr.To[int32](0)}))

		By("moving again by editing the claim the StatefulSet mounts, with no second replacement")
		uid := statefulSetFor(name).UID
		copyAndVerify(name, "second", "def")
		reconcileOnce(name)
		Expect(statefulSetFor(name).UID).To(Equal(uid))
		Expect(stateVolumeClaim(name)).To(Equal(targetClaimName(readAgent(name), "second")))
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonMoveStarting))
	})

	It("refuses a move whose last writer did not exit 0 or was released by hand, beside one that did, and creates nothing", func() {
		By("the control: a writer that exited 0")
		drainedAgent("move-clean", 0)
		setMove("move-clean", "a")
		reconcileOnce("move-clean")
		_, exists := claimOf(targetClaimName(readAgent("move-clean"), "a"))
		Expect(exists).To(BeTrue())

		By("a writer cut by SIGKILL")
		drainedAgent("move-killed", 137)
		setMove("move-killed", "a")
		reconcileOnce("move-killed")
		_, exists = claimOf(targetClaimName(readAgent("move-killed"), "a"))
		Expect(exists).To(BeFalse())
		Expect(moveCondition("move-killed").Reason).To(Equal(agentv1alpha1.ReasonWriterNotDrained))
		Expect(moveCondition("move-killed").Message).To(ContainSubstring("exited 137"))
		expectStoppedForMove("move-killed")

		By("a writer whose Pod a person released, which leaves its claim naming it running")
		name := "move-hand-released"
		createNode(name+"-node", corev1.ConditionTrue)
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		reconcileOnce(name)
		createClaim(name)
		pod := startPod(name, name+"-node", map[string]corev1.ContainerState{agentContainerName: runningState})
		reconcileOnce(name)
		releaseFinalizers(pod)
		deletePod(pod)
		Eventually(func() bool { _, exists := podFor(name); return exists }).Should(BeFalse())
		setMove(name, "a")
		reconcileOnce(name)
		_, exists = claimOf(targetClaimName(readAgent(name), "a"))
		Expect(exists).To(BeFalse())
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonWriterNotDrained))
		Expect(moveCondition(name).Message).To(ContainSubstring("never seen to stop"))
	})

	It("stops a running agent before anything of the move exists", func() {
		name := "move-running"
		createNode(name+"-node", corev1.ConditionTrue)
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		reconcileOnce(name)
		createClaim(name)
		startPod(name, name+"-node", map[string]corev1.ContainerState{agentContainerName: runningState})
		reconcileOnce(name)

		setMove(name, "a")
		reconcileOnce(name)
		expectStoppedForMove(name)
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonMoveStopping))
		_, exists := claimOf(targetClaimName(readAgent(name), "a"))
		Expect(exists).To(BeFalse())
	})

	It("refuses a move with no copy image, of a shared claim, or onto a claim it did not create, beside one it can run", func() {
		By("the control: the same move with a copy image creates its target")
		drainedAgent("move-imaged", 0)
		setMove("move-imaged", "a")
		agent := readAgent("move-imaged")
		imaged := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient, CopyImage: testCopyImage}
		plan, err := imaged.reconcileMove(ctx, agent, true, agentTypeSherlock)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.hold).To(BeTrue())
		_, exists := claimOf(targetClaimName(agent, "a"))
		Expect(exists).To(BeTrue())

		By("no copy image")
		drainedAgent("move-unimaged", 0)
		setMove("move-unimaged", "a")
		agent = readAgent("move-unimaged")
		unimaged := &AgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
		plan, err = unimaged.reconcileMove(ctx, agent, true, agentTypeSherlock)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.hold).To(BeTrue())
		Expect(meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionMemoryMove).Reason).
			To(Equal(agentv1alpha1.ReasonCopyImageUnset))
		_, exists = claimOf(targetClaimName(agent, "a"))
		Expect(exists).To(BeFalse())

		By("a workspace sharing the state claim")
		drainedAgent("move-shared", 0)
		_, err = reconcileAgentWithWorkspace("move-shared")
		Expect(err).NotTo(HaveOccurred())
		sharedShape("move-shared")
		setMove("move-shared", "a")
		reconcileOnce("move-shared")
		Expect(moveCondition("move-shared").Reason).To(Equal(agentv1alpha1.ReasonMoveSharedClaim))
		_, exists = claimOf(targetClaimName(readAgent("move-shared"), "a"))
		Expect(exists).To(BeFalse())

		By("a target claim of that name that no move created")
		drainedAgent("move-foreign", 0)
		foreign := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: targetClaimName(readAgent("move-foreign"), "a"), Namespace: agentNamespace},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		DeferCleanup(func() { removeClaim(foreign) })
		setMove("move-foreign", "a")
		reconcileOnce("move-foreign")
		reconcileOnce("move-foreign")
		Expect(moveCondition("move-foreign").Reason).To(Equal(agentv1alpha1.ReasonTargetForeign))
		expectStoppedForMove("move-foreign")
		_, exists = moveJobOf("move-foreign", "a", "copy")
		Expect(exists).To(BeFalse())
	})

	It("refuses a copy its verification does not match, keeping the source in use, the agent stopped and the target unused", func() {
		for _, refused := range []struct{ name, report, says string }{
			{"move-mismatch", "source=abc target=abd extra=0", "does not match"},
			{"move-source-moved", "source=abe target=abe extra=0", "source changed while it was copied"},
			{"move-extra", "source=abc target=abc extra=1", "beyond the copy"},
		} {
			By(refused.name)
			drainedAgent(refused.name, 0)
			setMove(refused.name, "a")
			reconcileOnce(refused.name)
			reconcileOnce(refused.name)
			copyJob, _ := moveJobOf(refused.name, "a", "copy")
			finishJob(copyJob, 0, "source=abc")
			reconcileOnce(refused.name)
			reconcileOnce(refused.name)
			verifyJob, _ := moveJobOf(refused.name, "a", "verify")
			finishJob(verifyJob, 0, refused.report)
			reconcileOnce(refused.name)

			target := targetClaimName(readAgent(refused.name), "a")
			Expect(phaseOf(target)).To(Equal(movePhaseRefused))
			Expect(moveCondition(refused.name).Reason).To(Equal(agentv1alpha1.ReasonVerificationFailed))
			Expect(moveCondition(refused.name).Message).To(ContainSubstring(refused.says))
			kept, _ := claimOf(stateVolumeName + "-" + refused.name + "-0")
			Expect(kept.Annotations).NotTo(HaveKey(movedToAnnotation))
			Expect(claimTemplate(statefulSetFor(refused.name), stateVolumeName)).NotTo(BeNil())
			expectStoppedForMove(refused.name)

			By("never using the refused id again")
			reconcileOnce(refused.name)
			Expect(phaseOf(target)).To(Equal(movePhaseRefused))
			expectStoppedForMove(refused.name)
		}
	})

	It("refuses a copy that failed or whose Job is gone, and a source written since the move began", func() {
		By("a copy that exited non-zero")
		drainedAgent("move-copy-failed", 0)
		setMove("move-copy-failed", "a")
		reconcileOnce("move-copy-failed")
		reconcileOnce("move-copy-failed")
		copyJob, _ := moveJobOf("move-copy-failed", "a", "copy")
		finishJob(copyJob, 1, "the store's writer lock is held")
		reconcileOnce("move-copy-failed")
		Expect(moveCondition("move-copy-failed").Reason).To(Equal(agentv1alpha1.ReasonCopyFailed))
		Expect(moveCondition("move-copy-failed").Message).To(ContainSubstring("writer lock is held"))

		By("a copy Job gone with no outcome")
		drainedAgent("move-copy-gone", 0)
		setMove("move-copy-gone", "a")
		reconcileOnce("move-copy-gone")
		reconcileOnce("move-copy-gone")
		copyJob, _ = moveJobOf("move-copy-gone", "a", "copy")
		Expect(k8sClient.Delete(ctx, copyJob)).To(Succeed())
		// envtest runs no garbage collector to take the orphan finalizer off.
		releaseFinalizers(copyJob)
		Eventually(func() bool { _, exists := moveJobOf("move-copy-gone", "a", "copy"); return exists }).Should(BeFalse())
		reconcileOnce("move-copy-gone")
		Expect(moveCondition("move-copy-gone").Reason).To(Equal(agentv1alpha1.ReasonCopyFailed))
		Expect(moveCondition("move-copy-gone").Message).To(ContainSubstring("is unknown"))

		By("a source with a writer since the move began")
		source := drainedAgent("move-source-written", 0)
		setMove("move-source-written", "a")
		reconcileOnce("move-source-written")
		written := source.DeepCopy()
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(source), written)).To(Succeed())
		before := written.DeepCopy()
		written.Annotations[lastWriterAnnotation] = `{"podUID":"another","state":"stopped","exitCode":0}`
		Expect(k8sClient.Patch(ctx, written, client.MergeFrom(before))).To(Succeed())
		reconcileOnce("move-source-written")
		Expect(moveCondition("move-source-written").Reason).To(Equal(agentv1alpha1.ReasonSourceChanged))
		_, exists := moveJobOf("move-source-written", "a", "copy")
		Expect(exists).To(BeFalse())
	})

	It("puts the memory back on the source when the agent exits on the copy before it serves", func() {
		name := "move-store-refused"
		source := drainedAgent(name, 0)
		copyAndVerify(name, "a", "abc")
		reconcileOnce(name)
		finishOrphaning(name)
		reconcileOnce(name)
		target := targetClaimName(readAgent(name), "a")
		Expect(stateVolumeClaim(name)).To(Equal(target))

		pod := startPod(name, name+"-node", map[string]corev1.ContainerState{agentContainerName: exitedState(1)})
		reconcileOnce(name)

		Expect(phaseOf(target)).To(Equal(movePhaseRefused))
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonAgentRefusedStore))
		Expect(moveCondition(name).Message).To(ContainSubstring("v0.2.0"))
		restored, _ := claimOf(source.Name)
		Expect(restored.Annotations).NotTo(HaveKey(movedToAnnotation))
		Expect(stateVolumeClaim(name)).To(Equal(source.Name))
		Expect(containerOf(statefulSetFor(name).Spec.Template.Spec, agentContainerName).Args).
			NotTo(ContainElement(sherlockRequireStoreFlag))
		expectStoppedForMove(name)
		Expect(pod.UID).NotTo(BeEmpty())
	})

	It("starts an Agent recreated after a move on the claim its memory moved to, not on the one it moved off", func() {
		name := "move-recreated"
		drainedAgent(name, 0)
		copyAndVerify(name, "a", "abc")
		reconcileOnce(name)
		finishOrphaning(name)
		reconcileOnce(name)
		target := targetClaimName(readAgent(name), "a")

		By("deleting the Agent and its StatefulSet, as a reconstruct finds them gone")
		agent := readAgent(name)
		releaseFinalizers(agent)
		Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
		Expect(k8sClient.Delete(ctx, statefulSetFor(name))).To(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(agent), &agentv1alpha1.Agent{})
		}).Should(MatchError(apierrors.IsNotFound, "a not-found error"))

		createAgent(newAgent(name))
		reconcileOnce(name)
		statefulSet := statefulSetFor(name)
		Expect(claimTemplate(statefulSet, stateVolumeName)).To(BeNil())
		Expect(stateVolumeClaim(name)).To(Equal(target))
		Expect(containerOf(statefulSet.Spec.Template.Spec, agentContainerName).Args).To(ContainElement(sherlockRequireStoreFlag))
	})

	It("holds the agent stopped while a Job of a move runs, even once the move is cleared", func() {
		name := "move-cleared"
		drainedAgent(name, 0)
		setMove(name, "a")
		reconcileOnce(name)
		reconcileOnce(name)
		_, exists := moveJobOf(name, "a", "copy")
		Expect(exists).To(BeTrue())

		agent := readAgent(name)
		cleared := agent.DeepCopy()
		cleared.Spec.MemoryMove = nil
		Expect(k8sClient.Patch(ctx, cleared, client.MergeFrom(agent))).To(Succeed())
		reconcileOnce(name)
		expectStoppedForMove(name)

		By("the control: the same Agent runs once the Job ended")
		copyJob, _ := moveJobOf(name, "a", "copy")
		finishJob(copyJob, 0, "source=abc")
		reconcileOnce(name)
		Expect(statefulSetFor(name).Spec.Replicas).To(HaveValue(BeEquivalentTo(1)))
		Expect(moveCondition(name).Reason).To(Equal(agentv1alpha1.ReasonNoMove))
	})

	It("refuses a target changed under the same id at admission", func() {
		name := "move-immutable"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		setMove(name, "a")
		agent := readAgent(name)
		changed := agent.DeepCopy()
		changed.Spec.MemoryMove.StorageSize = resource.MustParse("3Gi")
		Expect(k8sClient.Patch(ctx, changed, client.MergeFrom(agent))).To(MatchError(ContainSubstring("name a new id")))

		By("the control: a new id with another size is admitted")
		renamed := agent.DeepCopy()
		renamed.Spec.MemoryMove = &agentv1alpha1.MemoryMoveSpec{ID: "b", StorageSize: resource.MustParse("3Gi")}
		Expect(k8sClient.Patch(ctx, renamed, client.MergeFrom(agent))).To(Succeed())
	})
})
