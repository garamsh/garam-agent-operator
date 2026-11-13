package agent

import (
	"context"

	"github.com/garamsh/gaon/lib/api"
)

// SecretLister lists Secrets. It is the part of gaon's API that finds
// the model-key Secrets, which the Cluster's selector does not reach.
type SecretLister interface {
	ListSecrets(ctx context.Context) (*api.SecretList, error)
}

// Cluster is the part of gaon's resource API the operator calls, which
// *api.Client satisfies. The client it is given is built with
// api.WithLabelSelector(Selector()), so a list answers only what the
// operator owns, and an absent object is read by list, because a get is
// judged on no labels and a selector rule refuses it (ADR-0151 §3).
type Cluster interface {
	ListMicroVMs(ctx context.Context) (*api.MicroVMList, error)
	ListSecrets(ctx context.Context) (*api.SecretList, error)
	ListVolumes(ctx context.Context) (*api.VolumeList, error)
	PutMicroVM(ctx context.Context, m *api.MicroVM) (*api.MicroVM, error)
	PutSecret(ctx context.Context, s *api.Secret) (*api.Secret, error)
	PutVolume(ctx context.Context, v *api.Volume) (*api.Volume, error)
}
