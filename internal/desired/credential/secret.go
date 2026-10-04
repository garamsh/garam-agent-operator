// Package credential persists a managed agent's first certificate request and
// places the credential it is answered with, in Secrets beside the agent's
// Agent.
package credential

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// The keys of a request Secret. The key is under the name it keeps once it is
// placed.
const (
	requestKey   = "request.pem"
	epochKey     = "epoch"
	requestIDKey = "request-id"
)

// Secrets implements desired.CredentialStore in one namespace, the one the
// manager runs in.
//
// The request is persisted in its own Secret, mounted nowhere, rather than in the
// credential Secret: the workload is built once the credential Secret exists and
// copies it once, at Pod start (ADR 0010), so a credential Secret holding a key
// and no certificate would start the agent with a copy the certificate never
// reaches. The credential Secret is created whole, in one create, only once the
// certificate is in hand.
type Secrets struct {
	client    client.Client
	reader    client.Reader
	scheme    *runtime.Scheme
	namespace string
}

// NewSecrets returns a store writing through c and reading request Secrets'
// data through reader, which reads straight from the API server: the manager
// caches Secrets as metadata only, so no key material is held in its cache.
func NewSecrets(c client.Client, reader client.Reader, scheme *runtime.Scheme, namespace string) *Secrets {
	return &Secrets{client: c, reader: reader, scheme: scheme, namespace: namespace}
}

// Needed implements desired.CredentialStore.
func (s *Secrets) Needed(ctx context.Context) ([]desired.Need, error) {
	var agents agentv1alpha1.AgentList
	if err := s.client.List(ctx, &agents, client.InNamespace(s.namespace)); err != nil {
		return nil, fmt.Errorf("list the agents: %w", err)
	}

	var needs []desired.Need
	for i := range agents.Items {
		identity := agents.Items[i].Spec.Identity
		if identity == nil || identity.Source != agentv1alpha1.DesiredSourceControl {
			continue
		}
		placed, err := s.exists(ctx, agentname.CredentialsSecret(identity.GRN))
		if err != nil {
			return nil, err
		}
		if !placed {
			needs = append(needs, desired.Need{GRN: identity.GRN, Epoch: identity.AssignmentEpoch})

			continue
		}
		// A placement stopped between its two writes leaves the request
		// behind; the credential is placed, so the request is done with.
		if err := s.deleteRequest(ctx, identity.GRN); err != nil {
			return nil, err
		}
	}

	return needs, nil
}

// LoadRequest implements desired.CredentialStore.
func (s *Secrets) LoadRequest(ctx context.Context, agent string) (desired.PendingRequest, bool, error) {
	secret := &corev1.Secret{}
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.CredentialRequestSecret(agent)}, secret)
	if apierrors.IsNotFound(err) {
		return desired.PendingRequest{}, false, nil
	}
	if err != nil {
		return desired.PendingRequest{}, false, fmt.Errorf("get the certificate request of %s: %w", agent, err)
	}

	return desired.PendingRequest{
		KeyPEM: secret.Data[garam.KeyKey], CSRPEM: secret.Data[requestKey],
		Epoch: string(secret.Data[epochKey]), ID: string(secret.Data[requestIDKey]),
	}, true, nil
}

// SaveRequest implements desired.CredentialStore.
func (s *Secrets) SaveRequest(ctx context.Context, agent string, request desired.PendingRequest) error {
	data := map[string][]byte{
		garam.KeyKey: request.KeyPEM, requestKey: request.CSRPEM,
		epochKey: []byte(request.Epoch), requestIDKey: []byte(request.ID),
	}
	secret, err := s.ownedSecret(ctx, agent, agentname.CredentialRequestSecret(agent), data)
	if err != nil {
		return err
	}
	err = s.client.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) {
		// Only the epoch and the id ever change; the key and the request are
		// written once.
		existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secret.Name, Namespace: secret.Namespace}}
		updated := existing.DeepCopy()
		updated.Data = data
		err = s.client.Patch(ctx, updated, client.MergeFrom(existing))
	}
	if err != nil {
		return fmt.Errorf("persist the certificate request of %s: %w", agent, err)
	}

	return nil
}

// Place implements desired.CredentialStore. A credential Secret already there is
// left as it is: this operator places an agent's first credential and replaces
// none.
func (s *Secrets) Place(ctx context.Context, agent string, keyPEM []byte, certificate desired.Certificate) error {
	secret, err := s.ownedSecret(ctx, agent, agentname.CredentialsSecret(agent), map[string][]byte{
		garam.CertificateKey: certificate.CertificatePEM,
		garam.KeyKey:         keyPEM,
		garam.IssuerKey:      certificate.IssuerPEM,
		garam.ServerRootKey:  certificate.ServerRootPEM,
	})
	if err != nil {
		return err
	}
	if err := s.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("place the credential of %s: %w", agent, err)
	}

	return s.deleteRequest(ctx, agent)
}

// ownedSecret is a Secret named name holding data, owned by agent's Agent so
// that it goes when the Agent goes.
func (s *Secrets) ownedSecret(ctx context.Context, agent, name string, data map[string][]byte) (*corev1.Secret, error) {
	owner := &agentv1alpha1.Agent{}
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: agentname.Agent(agent)}, owner); err != nil {
		return nil, fmt.Errorf("get the agent of %s: %w", agent, err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	if err := controllerutil.SetControllerReference(owner, secret, s.scheme); err != nil {
		return nil, fmt.Errorf("own secret %s: %w", name, err)
	}

	return secret, nil
}

// exists reports whether a Secret is there, reading its metadata only.
func (s *Secrets) exists(ctx context.Context, name string) (bool, error) {
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, secret)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get secret %s: %w", name, err)
	}

	return true, nil
}

// deleteRequest removes agent's request Secret, where there is one.
func (s *Secrets) deleteRequest(ctx context.Context, agent string) error {
	present, err := s.exists(ctx, agentname.CredentialRequestSecret(agent))
	if err != nil || !present {
		return err
	}
	request := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agentname.CredentialRequestSecret(agent), Namespace: s.namespace}}
	if err := s.client.Delete(ctx, request); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove the certificate request of %s: %w", agent, err)
	}

	return nil
}
