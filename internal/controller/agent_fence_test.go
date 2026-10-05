package controller

import (
	"context"
	"errors"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
)

// fenceStates are the container states a spec gives an agent's Pod.
var (
	terminatedState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ContainerID: "containerd://stopped", ExitCode: 0,
	}}
	runningState = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
)

// podPatchRefused is the API server refusing every patch to a Pod, which is the
// one write a release makes after recording its evidence.
type podPatchRefused struct{ client.Client }

func (c podPatchRefused) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, isPod := obj.(*corev1.Pod); isPod && obj.GetDeletionTimestamp() != nil {
		return errors.New("refused")
	}

	return c.Client.Patch(ctx, obj, patch, opts...)
}

// createNode creates a node whose Ready condition is ready, and removes it when
// the spec ends.
func createNode(name string, ready corev1.ConditionStatus) {
	GinkgoHelper()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed()) })

	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}
	Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
}

// createClaim creates the claim the agent's StatefulSet would make for its Pod's
// state volume, and removes it when the spec ends.
func createClaim(agent string) *corev1.PersistentVolumeClaim {
	GinkgoHelper()

	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: stateVolumeName + "-" + agent + "-0", Namespace: agentNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	Expect(k8sClient.Create(ctx, claim)).To(Succeed())
	DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, claim))).To(Succeed()) })

	return claim
}

// removeClaim deletes a claim and waits until it is gone. The API server's
// protection finalizer would otherwise hold it until no Pod uses it, which no
// kubelet here will ever report.
func removeClaim(claim *corev1.PersistentVolumeClaim) {
	GinkgoHelper()

	releaseFinalizers(claim)
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, claim))).To(Succeed())
	Eventually(func() error {
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), &corev1.PersistentVolumeClaim{})
	}).Should(MatchError(apierrors.IsNotFound, "a not-found error"))
}

// startPod does what the StatefulSet controller and a kubelet would for the
// Agent: it creates the Pod its template describes on node, carrying the
// template's labels and finalizers, and reports its containers in states.
// envtest runs neither, so the spec stands in for both.
func startPod(agent, node string, states map[string]corev1.ContainerState) *corev1.Pod {
	GinkgoHelper()

	statefulSet := statefulSetFor(agent)
	pod := podOf(statefulSet, agentNamespace)
	pod.Labels = statefulSet.Spec.Template.Labels
	pod.Finalizers = statefulSet.Spec.Template.Finalizers
	pod.Spec.NodeName = node
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() {
		releaseFinalizers(pod)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed())
	})

	reportContainers(pod, states)

	return pod
}

// reportContainers writes the Pod's container statuses as a kubelet would, for
// its regular containers and for its init containers that keep running.
func reportContainers(pod *corev1.Pod, states map[string]corev1.ContainerState) {
	GinkgoHelper()

	current := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), current)).To(Succeed())
	current.Status.ContainerStatuses = nil
	current.Status.InitContainerStatuses = nil
	for _, container := range current.Spec.Containers {
		if state, ok := states[container.Name]; ok {
			current.Status.ContainerStatuses = append(current.Status.ContainerStatuses,
				corev1.ContainerStatus{Name: container.Name, State: state, Image: container.Image})
		}
	}
	for _, container := range current.Spec.InitContainers {
		if state, ok := states[container.Name]; ok {
			current.Status.InitContainerStatuses = append(current.Status.InitContainerStatuses,
				corev1.ContainerStatus{Name: container.Name, State: state, Image: container.Image})
		}
	}
	Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())
}

// deletePod deletes the Pod with no grace, which is the force-delete shape: the
// API server waits for no kubelet, and only the finalizer keeps the object.
func deletePod(pod *corev1.Pod) {
	GinkgoHelper()

	Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
}

// podFor reads the Agent's Pod back, and reports false where it is gone.
func podFor(agent string) (*corev1.Pod, bool) {
	GinkgoHelper()

	pod := &corev1.Pod{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: agent + "-0"}, pod)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	Expect(err).NotTo(HaveOccurred())

	return pod, true
}

// expectReleased asserts that the Agent's Pod was let go on evidence recorded
// first: the finalizer is gone, the evidence names the Pod, and the condition
// says so.
func expectReleased(agent string, podUID string) {
	GinkgoHelper()

	if pod, exists := podFor(agent); exists {
		Expect(controllerutil.ContainsFinalizer(pod, writerStoppedFinalizer)).To(BeFalse())
	}
	read := readAgent(agent)
	Expect(read.Status.WriterStopped).NotTo(BeNil())
	Expect(read.Status.WriterStopped.PodUID).To(Equal(podUID))
	fence := meta.FindStatusCondition(read.Status.Conditions, agentv1alpha1.ConditionWriterFence)
	Expect(fence).NotTo(BeNil())
	Expect(fence.Status).To(Equal(metav1.ConditionTrue))
	Expect(fence.Reason).To(Equal(agentv1alpha1.ReasonWriterStopped))
}

// expectHeld asserts that the Agent's Pod is still held, with no evidence
// recorded for it and the condition naming reason.
func expectHeld(agent, reason string) {
	GinkgoHelper()

	pod, exists := podFor(agent)
	Expect(exists).To(BeTrue(), "the Pod was let go")
	Expect(controllerutil.ContainsFinalizer(pod, writerStoppedFinalizer)).To(BeTrue())
	read := readAgent(agent)
	if read.Status.WriterStopped != nil {
		Expect(read.Status.WriterStopped.PodUID).NotTo(Equal(string(pod.UID)))
	}
	fence := meta.FindStatusCondition(read.Status.Conditions, agentv1alpha1.ConditionWriterFence)
	Expect(fence).NotTo(BeNil())
	Expect(fence.Status).To(Equal(metav1.ConditionUnknown))
	Expect(fence.Reason).To(Equal(reason))
	Expect(fence.Message).To(HavePrefix("Unverified"))
}

// fencedAgent creates an Agent and its workload, the node it runs on, and the
// claim of its volume, and starts its Pod there with every container in state.
// The first reconcile builds the StatefulSet; the second sees the Pod running
// and records the claim it started on.
func fencedAgent(name, node string, state corev1.ContainerState) *corev1.Pod {
	GinkgoHelper()

	createSecret(credentialsSecretName(name))
	createAgent(newAgent(name))
	_, err := reconcileAgent(name)
	Expect(err).NotTo(HaveOccurred())

	createClaim(name)
	pod := startPod(name, node, map[string]corev1.ContainerState{agentContainerName: state})
	_, err = reconcileAgent(name)
	Expect(err).NotTo(HaveOccurred())

	return pod
}

var _ = Describe("Writer fence", func() {
	It("puts the writer fence on every Pod the StatefulSet creates", func() {
		name := "fence-on-the-template"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		Expect(statefulSetFor(name).Spec.Template.Finalizers).To(ConsistOf(writerStoppedFinalizer))
	})

	It("releases a gracefully stopped Pod on evidence it records first, and holds a force-deleted one still running", func() {
		createNode("fence-node-graceful", corev1.ConditionTrue)

		By("the control: a Pod whose writer terminated")
		stopped := fencedAgent("fence-graceful", "fence-node-graceful", runningState)
		recorded, _ := podFor("fence-graceful")
		Expect(recorded.Annotations).To(HaveKey(pvcUIDAnnotation))
		reportContainers(stopped, map[string]corev1.ContainerState{agentContainerName: terminatedState})
		deletePod(stopped)
		_, err := reconcileAgent("fence-graceful")
		Expect(err).NotTo(HaveOccurred())
		expectReleased("fence-graceful", string(stopped.UID))
		evidence := readAgent("fence-graceful").Status.WriterStopped
		Expect(evidence.Containers).To(ConsistOf(HaveField("ContainerID", "containerd://stopped")))
		Expect(evidence.PVCUID).NotTo(BeEmpty())

		By("a Pod deleted while its writer still runs")
		running := fencedAgent("fence-force-deleted", "fence-node-graceful", runningState)
		deletePod(running)
		result, err := reconcileAgent("fence-force-deleted")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(fenceRecheckInterval))
		expectHeld("fence-force-deleted", agentv1alpha1.ReasonContainerRunning)
	})

	It("holds a Pod whose node is Unknown, beside one whose node is Ready", func() {
		createNode("fence-node-ready", corev1.ConditionTrue)
		createNode("fence-node-unknown", corev1.ConditionUnknown)

		ready := fencedAgent("fence-node-ready-control", "fence-node-ready", terminatedState)
		deletePod(ready)
		_, err := reconcileAgent("fence-node-ready-control")
		Expect(err).NotTo(HaveOccurred())
		expectReleased("fence-node-ready-control", string(ready.UID))

		unknown := fencedAgent("fence-node-unknown", "fence-node-unknown", terminatedState)
		deletePod(unknown)
		_, err = reconcileAgent("fence-node-unknown")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-node-unknown", agentv1alpha1.ReasonNodeUnknown)
	})

	It("holds a Pod whose recorded claim is not the claim now, beside one whose claim is unchanged", func() {
		createNode("fence-node-claim", corev1.ConditionTrue)

		same := fencedAgent("fence-claim-control", "fence-node-claim", terminatedState)
		deletePod(same)
		_, err := reconcileAgent("fence-claim-control")
		Expect(err).NotTo(HaveOccurred())
		expectReleased("fence-claim-control", string(same.UID))

		By("replacing the claim after the Pod recorded the first one")
		changed := fencedAgent("fence-claim-changed", "fence-node-claim", terminatedState)
		removeClaim(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: stateVolumeName + "-fence-claim-changed-0", Namespace: agentNamespace}})
		createClaim("fence-claim-changed")

		deletePod(changed)
		_, err = reconcileAgent("fence-claim-changed")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-claim-changed", agentv1alpha1.ReasonPVCChanged)
	})

	It("holds a Pod whose recorded claim was edited to a wrong value", func() {
		createNode("fence-node-annotation", corev1.ConditionTrue)

		edited := fencedAgent("fence-annotation-wrong", "fence-node-annotation", terminatedState)
		current, _ := podFor("fence-annotation-wrong")
		changedAnnotation := current.DeepCopy()
		changedAnnotation.Annotations[pvcUIDAnnotation] = "not-the-claim"
		Expect(k8sClient.Patch(ctx, changedAnnotation, client.MergeFrom(current))).To(Succeed())

		deletePod(edited)
		_, err := reconcileAgent("fence-annotation-wrong")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-annotation-wrong", agentv1alpha1.ReasonPVCChanged)
	})

	It("holds a Pod whose claim was created after it, even where the annotation was edited to match", func() {
		createNode("fence-node-later-claim", corev1.ConditionTrue)

		By("starting a Pod before its claim exists, as a claim recreated later would be")
		name := "fence-claim-later"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		pod := startPod(name, "fence-node-later-claim", map[string]corev1.ContainerState{agentContainerName: terminatedState})

		// Created in a later second than the Pod, which is what the rule reads.
		var claim *corev1.PersistentVolumeClaim
		Eventually(func(g Gomega) {
			if claim != nil {
				removeClaim(claim)
			}
			claim = createClaim(name)
			g.Expect(claim.CreationTimestamp.After(pod.CreationTimestamp.Time)).To(BeTrue())
		}).WithTimeout(10 * time.Second).Should(Succeed())

		By("editing the annotation to the claim's UID, so the annotation alone would pass")
		current, _ := podFor(name)
		matched := current.DeepCopy()
		if matched.Annotations == nil {
			matched.Annotations = map[string]string{}
		}
		matched.Annotations[pvcUIDAnnotation] = string(claim.UID)
		Expect(k8sClient.Patch(ctx, matched, client.MergeFrom(current))).To(Succeed())

		deletePod(pod)
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		expectHeld(name, agentv1alpha1.ReasonPVCChanged)
	})

	It("releases a Pod never scheduled to a node, and holds a scheduled one that reports no status", func() {
		createNode("fence-node-unreported", corev1.ConditionTrue)

		By("the control: a deleting Pod no node was ever assigned")
		name := "fence-never-scheduled"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		unscheduled := startPod(name, "", nil)
		deletePod(unscheduled)
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(unscheduled.UID))
		Expect(readAgent(name).Status.WriterStopped.Containers).To(BeEmpty())

		By("a scheduled Pod whose writer reports nothing")
		silent := fencedAgent("fence-no-status", "fence-node-unreported", terminatedState)
		reportContainers(silent, nil)
		deletePod(silent)
		_, err = reconcileAgent("fence-no-status")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-no-status", agentv1alpha1.ReasonNoContainerStatus)
	})

	It("holds a Pod whose writer terminated with no container ID, beside one whose writer reports its ID", func() {
		createNode("fence-node-container-id", corev1.ConditionTrue)

		By("the control: a writer terminated under a container ID")
		identified := fencedAgent("fence-container-id-control", "fence-node-container-id", terminatedState)
		deletePod(identified)
		_, err := reconcileAgent("fence-container-id-control")
		Expect(err).NotTo(HaveOccurred())
		expectReleased("fence-container-id-control", string(identified.UID))

		// The shape a Pod deleted before its container was created can report:
		// terminated, with no instance the evidence could name.
		By("a writer terminated with no container ID")
		anonymous := fencedAgent("fence-container-id-missing", "fence-node-container-id", runningState)
		reportContainers(anonymous, map[string]corev1.ContainerState{agentContainerName: {
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
		}})
		deletePod(anonymous)
		_, err = reconcileAgent("fence-container-id-missing")
		Expect(err).NotTo(HaveOccurred())
		expectHeld("fence-container-id-missing", agentv1alpha1.ReasonNoContainerStatus)
	})

	It("records the evidence before it lets the Pod go, so a release that fails there leaves both", func() {
		createNode("fence-node-persist", corev1.ConditionTrue)
		name := "fence-persisted-first"
		pod := fencedAgent(name, "fence-node-persist", terminatedState)
		deletePod(pod)

		By("reconciling with a client that refuses to release the Pod")
		_, err := runReconcile(name, &AgentReconciler{
			Client: podPatchRefused{k8sClient}, Scheme: k8sClient.Scheme(), CopyImage: testCopyImage,
		})
		Expect(err).To(MatchError(ContainSubstring("release the writer fence")))

		held, exists := podFor(name)
		Expect(exists).To(BeTrue())
		Expect(controllerutil.ContainsFinalizer(held, writerStoppedFinalizer)).To(BeTrue())
		Expect(readAgent(name).Status.WriterStopped).To(HaveField("PodUID", string(pod.UID)))

		By("the control: the same reconcile with a client that releases it")
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))
	})

	It("resumes a release interrupted after its evidence was recorded, without a second placement token", func() {
		createNode("fence-node-resume", corev1.ConditionTrue)
		name := "fence-resumed"
		pod := fencedAgent(name, "fence-node-resume", terminatedState)

		By("recording evidence as an interrupted release would, and leaving the finalizer")
		current := readAgent(name)
		recorded := current.DeepCopy()
		recorded.Status.WriterStopped = &agentv1alpha1.WriterStoppedEvidence{PodUID: string(pod.UID), ObservedAt: metav1.Now()}
		Expect(k8sClient.Status().Patch(ctx, recorded, client.MergeFrom(current))).To(Succeed())
		deletePod(pod)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))
		Expect(readAgent(name).Status.WriterStopped.Containers).NotTo(BeEmpty())
	})
})

var _ = Describe("Placement token", func() {
	It("mounts the token's copy into the adapter only, and its Secret into the copying init container only", func() {
		name := "token-adapter-only"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)
		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		mountsOf := func(container corev1.Container) []string {
			names := make([]string, 0, len(container.VolumeMounts))
			for _, mount := range container.VolumeMounts {
				names = append(names, mount.Name)
			}

			return names
		}

		By("the adapter reads the copy read-only at the token's path")
		adapter := initContainerOf(pod, adapterContainerName)
		Expect(adapter.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: placementVolumeName, MountPath: placementMountPath, ReadOnly: true}))
		Expect(mountsOf(adapter)).NotTo(ContainElement(placementSecretVolumeName))

		By("the copying init container reads the Secret and writes the copy, at the owner-only mode")
		copier := initContainerOf(pod, credentialsContainerName)
		Expect(mountsOf(copier)).To(ContainElements(placementSecretVolumeName, placementVolumeName))
		Expect(copier.Command[2]).To(ContainSubstring(placementSecretMountPath + "/*; do install -m 0600"))

		By("no other container mounts either")
		for _, container := range append(slices.Clone(pod.Containers), initContainerOf(pod, configContainerName)) {
			Expect(mountsOf(container)).NotTo(ContainElement(placementVolumeName), container.Name)
			Expect(mountsOf(container)).NotTo(ContainElement(placementSecretVolumeName), container.Name)
		}

		By("the copy lives in memory")
		Expect(volumeNamed(pod, placementVolumeName).EmptyDir).To(HaveField("Medium", corev1.StorageMediumMemory))
		Expect(volumeNamed(pod, placementSecretVolumeName).Secret.SecretName).To(Equal(agentname.PlacementSecret(name)))
	})

	It("mints a token for the first placement, keeps it across a restart in the same Pod, and mints the next one at the fence", func() {
		createNode("token-node", corev1.ConditionTrue)
		name := "token-per-placement"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)
		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())

		tokenOf := func() string {
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: agentname.PlacementSecret(name)}, secret)).
				To(Succeed())

			return string(secret.Data[placementTokenKey])
		}
		first := tokenOf()
		Expect(first).To(MatchRegexp("^[0-9a-f]{64}$"))
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx,
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.PlacementSecret(name), Namespace: agentNamespace}}))).
				To(Succeed())
		})

		createClaim(name)
		writers := map[string]corev1.ContainerState{agentContainerName: runningState, adapterContainerName: runningState}
		pod := startPod(name, "token-node", writers)

		By("a container restarting in the same Pod, which is the same placement")
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: runningState, adapterContainerName: terminatedState})
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(tokenOf()).To(Equal(first))

		By("the Pod stopping and its fence releasing, which is the next placement")
		reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: terminatedState, adapterContainerName: terminatedState})
		deletePod(pod)
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))
		Expect(tokenOf()).To(SatisfyAll(MatchRegexp("^[0-9a-f]{64}$"), Not(Equal(first))))
	})

	It("records on the next token the placement it replaces and the digest of the evidence its release recorded", func() {
		createNode("previous-node", corev1.ConditionTrue)
		name := "records-previous"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)
		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		placement := func() *corev1.Secret {
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: agentname.PlacementSecret(name)}, secret)).
				To(Succeed())

			return secret
		}
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx,
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.PlacementSecret(name), Namespace: agentNamespace}}))).
				To(Succeed())
		})

		By("the control: the first placement's token names no previous placement")
		Expect(placement().Annotations).NotTo(HaveKey(agentname.PreviousPodUIDAnnotation))
		Expect(placement().Annotations).NotTo(HaveKey(agentname.PreviousWriterStoppedAnnotation))

		createClaim(name)
		pod := startPod(name, "previous-node", map[string]corev1.ContainerState{
			agentContainerName: runningState, adapterContainerName: runningState,
		})
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: terminatedState, adapterContainerName: terminatedState})
		deletePod(pod)
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		expectReleased(name, string(pod.UID))

		By("the next token naming that Pod, and the digest of the evidence recorded for it, read back")
		recorded := readAgent(name).Status.WriterStopped
		digest, err := writerStoppedDigest(recorded)
		Expect(err).NotTo(HaveOccurred())
		Expect(placement().Annotations).To(HaveKeyWithValue(agentname.PreviousPodUIDAnnotation, string(pod.UID)))
		Expect(placement().Annotations).To(HaveKeyWithValue(agentname.PreviousWriterStoppedAnnotation, digest))
	})

	It("holds a Pod whose adapter still runs, though its agent stopped", func() {
		createNode("token-node-adapter", corev1.ConditionTrue)
		name := "fence-adapter-running"
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
		pod := startPod(name, "token-node-adapter",
			map[string]corev1.ContainerState{agentContainerName: terminatedState, adapterContainerName: runningState})
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())

		deletePod(pod)
		_, err = reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		expectHeld(name, agentv1alpha1.ReasonContainerRunning)
	})
})

var _ = Describe("Agent deletion", func() {
	It("keeps a deleting Agent until its fenced Pod is gone, and lets one with no Pod go at once", func() {
		By("the control: an Agent that never got a Pod")
		bare := "deleting-without-pod"
		createAgent(newAgent(bare))
		_, err := reconcileAgent(bare)
		Expect(err).NotTo(HaveOccurred())
		Expect(readAgent(bare).Finalizers).To(ContainElement(workloadFencedFinalizer))
		Expect(k8sClient.Delete(ctx, readAgent(bare))).To(Succeed())
		_, err = reconcileAgent(bare)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: bare}, &agentv1alpha1.Agent{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))

		By("an Agent whose Pod's writer still runs")
		createNode("deleting-node", corev1.ConditionTrue)
		name := "deleting-with-pod"
		pod := fencedAgent(name, "deleting-node", runningState)
		Expect(k8sClient.Delete(ctx, readAgent(name))).To(Succeed())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, &appsv1.StatefulSet{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))

		// envtest runs no garbage collector, so the spec deletes the Pod the
		// StatefulSet's deletion would have.
		deletePod(pod)
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(readAgent(name).DeletionTimestamp).NotTo(BeNil())
		expectHeld(name, agentv1alpha1.ReasonContainerRunning)

		By("its writer stopping, which releases the Pod and then the Agent")
		reportContainers(pod, map[string]corev1.ContainerState{agentContainerName: terminatedState})
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(readAgent(name).Status.WriterStopped.PodUID).To(Equal(string(pod.UID)))
		Eventually(func() bool { _, exists := podFor(name); return exists }).Should(BeFalse())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: agentNamespace, Name: name}, &agentv1alpha1.Agent{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))
	})
})
