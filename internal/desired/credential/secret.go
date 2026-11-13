// Package credential persists a managed agent's recovery request, and records
// the lineage its recovery is answered with on the agent's credential Secret.
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
)

// The keys of a recovery request Secret.
const (
	epochKey     = "epoch"
	requestIDKey = "request-id"
)

// Secrets implements desired.RecoveryStore in one namespace, the one the
// manager runs in. The request is persisted in its own Secret, mounted nowhere.
type Secrets struct {
	client    client.Client
	reader    client.Reader
	scheme    *runtime.Scheme
	namespace string
}

// NewSecrets returns a store writing through c and reading request Secrets'
// data through reader, which reads straight from the API server: the manager
// caches Secrets as metadata only.
func NewSecrets(c client.Client, reader client.Reader, scheme *runtime.Scheme, namespace string) *Secrets {
	return &Secrets{client: c, reader: reader, scheme: scheme, namespace: namespace}
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
