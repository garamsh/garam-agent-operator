// Package credentialstore keeps this operator's garam credential in the Secret
// its own Pod mounts.
package credentialstore

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// Secret replaces the credential in the Secret the manager's Pod mounts it
// from. The kubelet projects the change into the mounted volume, and the
// handshake that follows reads it — so the file the process reads and the
// object this writes are two ends of one store rather than two stores.
//
// The write is a patch and there is no read: this operator learns its
// credential from the mounted file and never from the API, which is why it
// holds no permission to get the Secret it writes.
type Secret struct {
	client client.Client
	secret types.NamespacedName

	// certificateKey and keyKey are the Secret's keys, which are also the
	// names the kubelet gives the files in the volume.
	certificateKey string
	keyKey         string
}

// NewSecret returns a Secret replacing the credential under certificateKey and
// keyKey of the named Secret.
func NewSecret(c client.Client, secret types.NamespacedName, certificateKey, keyKey string) *Secret {
	return &Secret{client: c, secret: secret, certificateKey: certificateKey, keyKey: keyKey}
}

// ReplaceCredential writes credential into the Secret, replacing both keys in
// one patch so that no reader sees a certificate beside another's key.
func (s *Secret) ReplaceCredential(ctx context.Context, credential garam.Credential) error {
	patch, err := json.Marshal(map[string]any{"data": map[string][]byte{
		s.certificateKey: credential.CertificatePEM,
		s.keyKey:         credential.KeyPEM,
	}})
	if err != nil {
		return fmt.Errorf("render the credential for secret %s: %w", s.secret, err)
	}

	secret := &corev1.Secret{}
	secret.Namespace, secret.Name = s.secret.Namespace, s.secret.Name
	if err := s.client.Patch(ctx, secret, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("replace the credential in secret %s: %w", s.secret, err)
	}
	return nil
}
