package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

const (
	// writerStoppedFinalizer holds a deleting agent Pod until every container
	// that could write the agent's state is positively seen to have stopped.
	// Kubernetes force deletion does not wait for the kubelet, so a Pod that is
	// gone is no evidence its writers stopped; this finalizer is what keeps the
	// StatefulSet from creating the next Pod on the same volume until there is.
	writerStoppedFinalizer = "agent.garam.sh/writer-stopped"

	// workloadFencedFinalizer holds a deleting Agent until its Pod is gone, so
	// that the Pod's fence still has an Agent to record its evidence on.
	workloadFencedFinalizer = "agent.garam.sh/workload-fenced"

	// pvcUIDAnnotation records on the Pod the UID of the state volume's claim
	// the first time the controller sees the Pod running. It is fence evidence,
	// kept on the object the fence holds rather than in the Agent's status,
	// which the controller does not read back.
	pvcUIDAnnotation = "agent.garam.sh/pvc-uid"

	// placementSecretSuffix names the Secret holding an Agent's placement token
	// after the Agent, and placementTokenKey is its one key.
	placementSecretSuffix = "-placement"
	placementTokenKey     = "token"

	// placementTokenBytes is how much randomness a placement token carries.
	placementTokenBytes = 32

	// fenceRecheckInterval is how soon an unverified fence is read again. It
	// re-reads and never releases: a node's state is not watched, so a node
	// that recovers is only noticed by looking again.
	fenceRecheckInterval = 30 * time.Second
)

// fenceVerdict is the outcome of reading a deleting Pod: the evidence that its
// writers stopped, or why there is none.
type fenceVerdict struct {
	evidence *agentv1alpha1.WriterStoppedEvidence
	reason   string
	message  string
}

// agentPodName is the one Pod an Agent's StatefulSet creates.
func agentPodName(agent *agentv1alpha1.Agent) string { return agent.Name + "-0" }

// stateClaimName is the claim the StatefulSet makes for that Pod's state volume.
func stateClaimName(agent *agentv1alpha1.Agent) string {
	return stateVolumeName + "-" + agentPodName(agent)
}

// placementSecretName is the Secret an Agent's placement token is minted into.
func placementSecretName(agent *agentv1alpha1.Agent) string {
	return agent.Name + placementSecretSuffix
}

// reconcileFence reads the Agent's Pod and acts on its fence. A running Pod has
// its placement recorded; a deleting one is released only on positive evidence
// that its writers stopped, recorded on the Agent first. It reports whether the
// Pod is gone and whether the fence is unverified and worth reading again.
func (r *AgentReconciler) reconcileFence(ctx context.Context, agent *agentv1alpha1.Agent) (podGone, unverified bool, err error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: agentPodName(agent)}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return true, false, nil
		}

		return false, false, fmt.Errorf("get the agent's pod: %w", err)
	}

	if pod.DeletionTimestamp == nil {
		return false, false, r.recordPlacement(ctx, agent, pod)
	}
	// Released already, by this controller or by a person.
	if !controllerutil.ContainsFinalizer(pod, writerStoppedFinalizer) {
		return false, false, nil
	}

	verdict, err := r.readFence(ctx, agent, pod)
	if err != nil {
		return false, false, err
	}
	if verdict.evidence == nil {
		setWriterFence(agent, metav1.ConditionUnknown, verdict.reason,
			fmt.Sprintf("Unverified: Pod %q (%s) is held. %s. It is released when every writing container is seen terminated, or by a person; never on a timeout",
				pod.Name, pod.UID, verdict.message))

		return false, true, nil
	}

	return false, false, r.releaseFence(ctx, agent, pod, verdict.evidence)
}

// releaseFence records the evidence on the Agent, mints the next placement's
// token, and only then lets the Pod go. Each step is safe to repeat, so a
// controller stopped between two of them resumes by reading the Pod again: it
// is still deleting and still terminated, and nothing new can start on the
// volume until the last step.
func (r *AgentReconciler) releaseFence(ctx context.Context, agent *agentv1alpha1.Agent, pod *corev1.Pod,
	evidence *agentv1alpha1.WriterStoppedEvidence) error {
	before := agent.DeepCopy()
	agent.Status.WriterStopped = evidence
	if err := r.Status().Patch(ctx, agent, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("record the writer-stopped evidence: %w", err)
	}

	// The next Pod the StatefulSet creates is the next placement, and it copies
	// the token at start; minting it here is what makes it that placement's.
	if r.adapterBuilt(agent) {
		if err := r.mintPlacementToken(ctx, agent); err != nil {
			return err
		}
	}

	released := pod.DeepCopy()
	controllerutil.RemoveFinalizer(released, writerStoppedFinalizer)
	if err := r.Patch(ctx, released, client.MergeFrom(pod)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("release the writer fence on pod %q: %w", pod.Name, err)
	}

	setWriterFence(agent, metav1.ConditionTrue, agentv1alpha1.ReasonWriterStopped,
		fmt.Sprintf("Pod %q (%s) was released on the evidence recorded in status.writerStopped", pod.Name, pod.UID))
	logf.FromContext(ctx).Info("Released the writer fence on evidence", "pod", pod.Name, "podUID", pod.UID)

	return nil
}

// readFence decides whether a deleting Pod's writers have positively stopped.
// The node is read first because a node that is gone or unreachable leaves the
// container states it last reported stale, and the claim next because evidence
// about a volume other than the one the Pod started on is evidence about
// nothing.
func (r *AgentReconciler) readFence(ctx context.Context, agent *agentv1alpha1.Agent, pod *corev1.Pod) (fenceVerdict, error) {
	now := metav1.Now()

	// A Pod no node was ever assigned started nothing. That is the one form
	// "never started" takes as evidence; an empty status on a scheduled Pod is
	// not it.
	if pod.Spec.NodeName == "" {
		return fenceVerdict{evidence: &agentv1alpha1.WriterStoppedEvidence{
			PodUID: string(pod.UID), ObservedAt: now,
		}}, nil
	}

	if reason, message, err := r.readNode(ctx, pod.Spec.NodeName); err != nil || reason != "" {
		return fenceVerdict{reason: reason, message: message}, err
	}

	pvcUID, reason, message, err := r.readClaim(ctx, agent, pod)
	if err != nil || reason != "" {
		return fenceVerdict{reason: reason, message: message}, err
	}

	containers, reason, message := terminatedWriters(pod)
	if reason != "" {
		return fenceVerdict{reason: reason, message: message}, nil
	}

	return fenceVerdict{evidence: &agentv1alpha1.WriterStoppedEvidence{
		PodUID: string(pod.UID), PVCUID: pvcUID, Containers: containers, ObservedAt: now,
	}}, nil
}

// readNode reads the Pod's node uncached, so that the controller holds no
// informer over every node in the cluster.
func (r *AgentReconciler) readNode(ctx context.Context, name string) (reason, message string, err error) {
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return agentv1alpha1.ReasonNodeUnknown, fmt.Sprintf("Node %q no longer exists", name), nil
		}

		return "", "", fmt.Errorf("get node %q: %w", name, err)
	}

	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		if condition.Status == corev1.ConditionUnknown {
			return agentv1alpha1.ReasonNodeUnknown,
				fmt.Sprintf("Node %q's Ready condition is Unknown, so the container states it reported may be stale", name), nil
		}

		return "", "", nil
	}

	return agentv1alpha1.ReasonNodeUnknown, fmt.Sprintf("Node %q reports no Ready condition", name), nil
}

// readClaim checks that the state volume's claim is the one the Pod started on.
// Two records say so: the annotation the controller wrote on the Pod, and the
// API server's creation times, which nobody can edit. The claim is made before
// its Pod, so one created after the Pod replaced it, whatever the annotation
// says.
func (r *AgentReconciler) readClaim(ctx context.Context, agent *agentv1alpha1.Agent,
	pod *corev1.Pod) (pvcUID, reason, message string, err error) {
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: stateClaimName(agent)}, claim); err != nil {
		if apierrors.IsNotFound(err) {
			return "", agentv1alpha1.ReasonPVCChanged, fmt.Sprintf("Claim %q no longer exists", stateClaimName(agent)), nil
		}

		return "", "", "", fmt.Errorf("get claim %q: %w", stateClaimName(agent), err)
	}

	recorded := pod.Annotations[pvcUIDAnnotation]
	if recorded != string(claim.UID) {
		return "", agentv1alpha1.ReasonPVCChanged,
			fmt.Sprintf("Claim %q is %s, and the Pod recorded %q", claim.Name, claim.UID, recorded), nil
	}
	if claim.CreationTimestamp.After(pod.CreationTimestamp.Time) {
		return "", agentv1alpha1.ReasonPVCChanged,
			fmt.Sprintf("Claim %q was created after the Pod", claim.Name), nil
	}

	return string(claim.UID), "", "", nil
}

// terminatedWriters returns the terminated state of every container that could
// write the agent's state: each regular container, and each init container
// that keeps running beside them. An init container that ran to completion
// before the agent started holds nothing of the store.
func terminatedWriters(pod *corev1.Pod) ([]agentv1alpha1.TerminatedContainer, string, string) {
	statuses := map[string]corev1.ContainerStatus{}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...),
		pod.Status.ContainerStatuses...) {
		statuses[status.Name] = status
	}

	writers := make([]string, 0, len(pod.Spec.Containers)+1)
	for _, container := range pod.Spec.InitContainers {
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			writers = append(writers, container.Name)
		}
	}
	for _, container := range pod.Spec.Containers {
		writers = append(writers, container.Name)
	}

	terminated := make([]agentv1alpha1.TerminatedContainer, 0, len(writers))
	for _, name := range writers {
		status, reported := statuses[name]
		switch {
		case !reported:
			return nil, agentv1alpha1.ReasonNoContainerStatus,
				fmt.Sprintf("Container %q reports no status", name)
		case status.State.Running != nil:
			return nil, agentv1alpha1.ReasonContainerRunning,
				fmt.Sprintf("Container %q is running", name)
		case status.State.Waiting != nil:
			return nil, agentv1alpha1.ReasonContainerWaiting,
				fmt.Sprintf("Container %q is waiting", name)
		case status.State.Terminated == nil || status.State.Terminated.ContainerID == "":
			return nil, agentv1alpha1.ReasonNoContainerStatus,
				fmt.Sprintf("Container %q reports no terminated instance", name)
		}

		state := status.State.Terminated
		terminated = append(terminated, agentv1alpha1.TerminatedContainer{
			Name: name, ContainerID: state.ContainerID, ExitCode: state.ExitCode, FinishedAt: state.FinishedAt,
		})
	}

	return terminated, "", ""
}

// recordPlacement records on a running Pod the claim it started on, once, and
// reports the placement on the Agent. The annotation is written only where it
// is absent, so a value already there is never replaced from the cluster's
// current state.
func (r *AgentReconciler) recordPlacement(ctx context.Context, agent *agentv1alpha1.Agent, pod *corev1.Pod) error {
	recorded, annotated := pod.Annotations[pvcUIDAnnotation]
	if !annotated {
		claim := &corev1.PersistentVolumeClaim{}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: stateClaimName(agent)}, claim)
		if apierrors.IsNotFound(err) {
			// The StatefulSet makes the claim before the Pod; a Pod with none is
			// one this reconcile sees early, and the next records it.
			return nil
		}
		if err != nil {
			return fmt.Errorf("get claim %q: %w", stateClaimName(agent), err)
		}

		marked := pod.DeepCopy()
		if marked.Annotations == nil {
			marked.Annotations = map[string]string{}
		}
		marked.Annotations[pvcUIDAnnotation] = string(claim.UID)
		if err := r.Patch(ctx, marked, client.MergeFrom(pod)); err != nil {
			return fmt.Errorf("record the claim on pod %q: %w", pod.Name, err)
		}
		recorded = string(claim.UID)
	}

	agent.Status.Placement = &agentv1alpha1.Placement{PodUID: string(pod.UID), PVCUID: recorded}

	return nil
}

// ensurePlacementToken mints the first placement's token where the Agent has
// none, before its first Pod is created. Later placements are minted when the
// previous Pod's fence is released.
func (r *AgentReconciler) ensurePlacementToken(ctx context.Context, agent *agentv1alpha1.Agent) error {
	existing := &metav1.PartialObjectMetadata{}
	existing.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: placementSecretName(agent)}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get the placement token secret: %w", err)
	}

	token, err := newPlacementToken()
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: placementSecretName(agent), Namespace: agent.Namespace},
		Data:       map[string][]byte{placementTokenKey: token},
	}
	if err := controllerutil.SetControllerReference(agent, secret, r.Scheme); err != nil {
		return fmt.Errorf("own the placement token secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create the placement token secret: %w", err)
	}

	return nil
}

// mintPlacementToken replaces the token in the Agent's placement Secret, or
// creates it, with a new random one. It names no resource version: the token
// is this controller's alone, and any newer value is as good as this one.
func (r *AgentReconciler) mintPlacementToken(ctx context.Context, agent *agentv1alpha1.Agent) error {
	token, err := newPlacementToken()
	if err != nil {
		return err
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: placementSecretName(agent), Namespace: agent.Namespace}}
	minted := secret.DeepCopy()
	minted.Data = map[string][]byte{placementTokenKey: token}
	err = r.Patch(ctx, minted, client.MergeFrom(secret))
	if apierrors.IsNotFound(err) {
		return r.ensurePlacementToken(ctx, agent)
	}
	if err != nil {
		return fmt.Errorf("mint the placement token: %w", err)
	}

	return nil
}

// newPlacementToken is a random token, hex-encoded so the file holding it is
// text.
func newPlacementToken() ([]byte, error) {
	raw := make([]byte, placementTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("read randomness for a placement token: %w", err)
	}

	return []byte(hex.EncodeToString(raw)), nil
}
