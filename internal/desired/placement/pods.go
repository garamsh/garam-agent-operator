// Package placement reads managed agents' current placements off their Pods and
// placement Secrets, for the registrar to register with the control service.
package placement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// Pods implements desired.PlacementStore in one namespace, the one the manager
// runs in.
type Pods struct {
	client    client.Client
	reader    client.Reader
	namespace string
}

// NewPods returns a store reading Agents, Pods and the placement Secrets'
// metadata through c, and a placement token through reader, which reads straight
// from the API server: the manager caches Secrets as metadata only, so no token
// is held in its cache.
func NewPods(c client.Client, reader client.Reader, namespace string) *Pods {
	return &Pods{client: c, reader: reader, namespace: namespace}
}

// Current implements desired.PlacementStore. Nothing here reads an Agent's
// status: the epoch is the spec's, the claim is the one the writer fence
// recorded on the Pod, and the previous placement is the one the release that
// minted the token recorded on the token's Secret.
func (p *Pods) Current(ctx context.Context) ([]desired.CurrentPlacement, error) {
	var agents agentv1alpha1.AgentList
	if err := p.client.List(ctx, &agents, client.InNamespace(p.namespace)); err != nil {
		return nil, fmt.Errorf("list the agents: %w", err)
	}

	var current []desired.CurrentPlacement
	for i := range agents.Items {
		agent := &agents.Items[i]
		identity := agent.Spec.Identity
		if identity == nil || identity.Source != agentv1alpha1.DesiredSourceControl || identity.AssignmentEpoch == "" {
			continue
		}
		placement, ok, err := p.placementOf(ctx, agent)
		if err != nil {
			return nil, err
		}
		if ok {
			current = append(current, desired.CurrentPlacement{GRN: identity.GRN, Placement: placement})
		}
	}

	return current, nil
}

// placementOf is the Agent's current placement, and false where it has none: no
// Pod running that is not being deleted, no claim recorded on it yet, or no
// adapter in it, which is the one container a placement is for.
func (p *Pods) placementOf(ctx context.Context, agent *agentv1alpha1.Agent) (desired.Placement, bool, error) {
	pod := &corev1.Pod{}
	err := p.client.Get(ctx, client.ObjectKey{Namespace: p.namespace, Name: agent.Name + "-0"}, pod)
	if apierrors.IsNotFound(err) {
		return desired.Placement{}, false, nil
	}
	if err != nil {
		return desired.Placement{}, false, fmt.Errorf("get the pod of %s: %w", agent.Name, err)
	}
	pvcUID := pod.Annotations[agentname.PVCUIDAnnotation]
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pvcUID == "" || !carriesAdapter(pod) {
		return desired.Placement{}, false, nil
	}

	secret := &corev1.Secret{}
	err = p.reader.Get(ctx, client.ObjectKey{Namespace: p.namespace, Name: agentname.PlacementSecret(agent.Name)}, secret)
	if apierrors.IsNotFound(err) {
		return desired.Placement{}, false, nil
	}
	if err != nil {
		return desired.Placement{}, false, fmt.Errorf("get the placement token of %s: %w", agent.Name, err)
	}
	token := secret.Data[agentname.PlacementTokenKey]
	if len(token) == 0 {
		return desired.Placement{}, false, nil
	}
	sum := sha256.Sum256(token)

	placement := desired.Placement{
		Epoch: agent.Spec.Identity.AssignmentEpoch, PodUID: string(pod.UID), PVCUID: pvcUID,
		TokenSHA256: hex.EncodeToString(sum[:]),
	}
	if previous := secret.Annotations[agentname.PreviousPodUIDAnnotation]; previous != "" {
		placement.Previous = &desired.PreviousPlacement{
			PodUID: previous, WriterStoppedSHA256: secret.Annotations[agentname.PreviousWriterStoppedAnnotation],
		}
	}

	return placement, true, nil
}

// carriesAdapter reports whether the Pod runs garam's adapter.
func carriesAdapter(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.InitContainers {
		if container.Name == agentname.AdapterContainer {
			return true
		}
	}

	return false
}
