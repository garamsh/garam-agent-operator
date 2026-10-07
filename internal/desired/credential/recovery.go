package credential

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/garam"
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
		if identity == nil || identity.Source != agentv1alpha1.DesiredSourceControl {
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
func (s *Secrets) LoadRecovery(ctx context.Context, agent string) (desired.PendingRequest, bool, error) {
	secret := &corev1.Secret{}
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.RecoveryRequestSecret(agent)}, secret)
	if apierrors.IsNotFound(err) {
		return desired.PendingRequest{}, false, nil
	}
	if err != nil {
		return desired.PendingRequest{}, false, fmt.Errorf("get the recovery request of %s: %w", agent, err)
	}

	return desired.PendingRequest{
		KeyPEM: secret.Data[garam.KeyKey], CSRPEM: secret.Data[requestKey],
		Epoch: string(secret.Data[epochKey]), ID: string(secret.Data[requestIDKey]),
	}, true, nil
}

// SaveRecovery implements desired.RecoveryStore. It creates the request
// Secret, and leaves one already there as it is: that key is the one garam may
// have signed over.
func (s *Secrets) SaveRecovery(ctx context.Context, agent string, request desired.PendingRequest) error {
	secret, err := s.ownedSecret(ctx, agent, agentname.RecoveryRequestSecret(agent), map[string][]byte{
		garam.KeyKey: request.KeyPEM, requestKey: request.CSRPEM,
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

// KeptIssuer implements desired.RecoveryStore.
func (s *Secrets) KeptIssuer(ctx context.Context, agent string) ([]byte, bool, error) {
	secret := &corev1.Secret{}
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.CredentialsSecret(agent)}, secret)
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get the credential of %s: %w", agent, err)
	}

	return secret.Data[garam.IssuerKey], true, nil
}

// RefuseRecovery implements desired.RecoveryStore.
func (s *Secrets) RefuseRecovery(ctx context.Context, agent, reason string) error {
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.RecoveryRequestSecret(agent), Namespace: s.namespace}}
	refused := existing.DeepCopy()
	refused.Annotations = map[string]string{agentname.RecoveryRefusedAnnotation: reason}
	if err := s.client.Patch(ctx, refused, client.MergeFrom(existing)); err != nil {
		return fmt.Errorf("record the refusal of the recovery of %s: %w", agent, err)
	}

	return nil
}

// PlaceRecovered implements desired.RecoveryStore. On a placed credential the
// patch names the certificate, the key, the lineage, and the issuer and server
// root only where certificate names them, so a chain it does not name is kept as
// it is. With no credential placed, the credential is created whole, chain
// included.
func (s *Secrets) PlaceRecovered(ctx context.Context, agent string, keyPEM []byte, certificate desired.Certificate,
	lineage string) error {
	data := map[string][]byte{garam.CertificateKey: certificate.CertificatePEM, garam.KeyKey: keyPEM}
	if len(certificate.IssuerPEM) > 0 && len(certificate.ServerRootPEM) > 0 {
		data[garam.IssuerKey], data[garam.ServerRootKey] = certificate.IssuerPEM, certificate.ServerRootPEM
	}
	placed, err := s.exists(ctx, agentname.CredentialsSecret(agent))
	if err != nil {
		return err
	}
	if placed {
		existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.CredentialsSecret(agent), Namespace: s.namespace}}
		recovered := existing.DeepCopy()
		recovered.Annotations = map[string]string{agentname.CredentialLineageAnnotation: lineage}
		recovered.Data = data
		if err := s.client.Patch(ctx, recovered, client.MergeFrom(existing)); err != nil {
			return fmt.Errorf("place the recovered credential of %s: %w", agent, err)
		}
	} else {
		if len(data) != 4 {
			return fmt.Errorf("place the recovered credential of %s: no credential is placed and no chain is answered", agent)
		}
		secret, err := s.ownedSecret(ctx, agent, agentname.CredentialsSecret(agent), data)
		if err != nil {
			return err
		}
		secret.Annotations = map[string]string{agentname.CredentialLineageAnnotation: lineage}
		if err := s.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("place the recovered credential of %s: %w", agent, err)
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
