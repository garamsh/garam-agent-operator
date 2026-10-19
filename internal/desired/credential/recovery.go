package credential

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
)

var _ desired.RecoveryStore = (*Secrets)(nil)

// Recovering implements desired.RecoveryStore.
func (s *Secrets) Recovering(ctx context.Context) ([]string, error) {
	var agents agentv1alpha1.AgentList
	if err := s.client.List(ctx, &agents, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list the agents: %w", err)
	}

	var recovering []string
	for i := range agents.Items {
		identity := agents.Items[i].Spec.Identity
		if identity == nil {
			continue
		}
		persisted, err := s.exists(ctx, agentname.RecoveryRequestSecret(identity.GRN))
		if err != nil {
			return nil, err
		}
		if persisted {
			recovering = append(recovering, identity.GRN)
		}
	}

	return recovering, nil
}

// LoadRecovery implements desired.RecoveryStore.
func (s *Secrets) LoadRecovery(ctx context.Context, agent string) (desired.RecoveryRequest, bool, error) {
	secret := &corev1.Secret{}
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.RecoveryRequestSecret(agent)}, secret)
	if apierrors.IsNotFound(err) {
		return desired.RecoveryRequest{}, false, nil
	}
	if err != nil {
		return desired.RecoveryRequest{}, false, fmt.Errorf("get the recovery request of %s: %w", agent, err)
	}

	return desired.RecoveryRequest{Epoch: string(secret.Data[epochKey]), ID: string(secret.Data[requestIDKey])}, true, nil
}

// SaveRecovery implements desired.RecoveryStore. It creates the request
// Secret, and leaves one already there as it is.
func (s *Secrets) SaveRecovery(ctx context.Context, agent string, request desired.RecoveryRequest) error {
	secret, err := s.ownedSecret(ctx, agent, agentname.RecoveryRequestSecret(agent), map[string][]byte{
		epochKey: []byte(request.Epoch), requestIDKey: []byte(request.ID),
	})
	if err != nil {
		return err
	}
	if err := s.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("persist the recovery request of %s: %w", agent, err)
	}

	return nil
}

// Recovered implements desired.RecoveryStore. One merge patch records the
// lineage on the credential Secret and removes its refresh time, so the
// refresher mints the agent's tokens again on its next pass, in the new
// lineage; its own write of tokens it minted before is refused, because that
// write names the version it read (ADR 0073).
func (s *Secrets) Recovered(ctx context.Context, agent, lineage string) error {
	// Read uncached: a credential the cache does not show yet would go
	// unrecorded, and keep tokens of the lineage the recovery ended.
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.CredentialsSecret(agent)}, secret)
	placed := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get the credential of %s: %w", agent, err)
	}
	if placed {
		patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{
			agentname.CredentialLineageAnnotation: lineage,
			agentname.TokensRefreshAnnotation:     nil,
		}}})
		if err != nil {
			return fmt.Errorf("render the recovered lineage of %s: %w", agent, err)
		}
		credential := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.CredentialsSecret(agent), Namespace: s.namespace}}
		if err := s.client.Patch(ctx, credential, client.RawPatch(types.MergePatchType, patch)); err != nil {
			return fmt.Errorf("record the recovered lineage of %s: %w", agent, err)
		}
	}

	return s.deleteSecret(ctx, agentname.RecoveryRequestSecret(agent))
}

// deleteSecret removes the Secret called name, where there is one.
func (s *Secrets) deleteSecret(ctx context.Context, name string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace}}
	if err := s.client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove secret %s: %w", name, err)
	}

	return nil
}
