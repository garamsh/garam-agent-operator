package constructor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// Tokens implements garam.TokenStore in the namespace the manager runs in. It
// reads each credential Secret's metadata and nothing else, so no part of this
// operator reads back the tokens it wrote.
type Tokens struct {
	client    client.Client
	scheme    *runtime.Scheme
	namespace string
}

var _ garam.TokenStore = (*Tokens)(nil)

// NewTokens returns a Tokens writing through c into namespace.
func NewTokens(c client.Client, scheme *runtime.Scheme, namespace string) *Tokens {
	return &Tokens{client: c, scheme: scheme, namespace: namespace}
}

// Holders implements garam.TokenStore.
func (t *Tokens) Holders(ctx context.Context) ([]garam.TokenHolder, error) {
	var agents agentv1alpha1.AgentList
	if err := t.client.List(ctx, &agents, client.InNamespace(t.namespace)); err != nil {
		return nil, fmt.Errorf("list the agents: %w", err)
	}
	var holders []garam.TokenHolder
	for i := range agents.Items {
		identity := agents.Items[i].Spec.Identity
		if identity == nil {
			continue
		}
		holder := garam.TokenHolder{
			Agent:  garam.GRN(identity.GRN),
			Fenced: identity.Source == agentv1alpha1.DesiredSourceControl,
		}
		if identity.AssignmentEpoch != "" {
			epoch, err := strconv.ParseInt(identity.AssignmentEpoch, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("read the epoch of %s: %w", identity.GRN, err)
			}
			holder.Epoch = epoch
		}
		if err := t.readDue(ctx, &holder); err != nil {
			return nil, err
		}
		holders = append(holders, holder)
	}
	return holders, nil
}

// readDue reads when the holder's tokens are next due, and the version of the
// Secret that says so, off the credential Secret's metadata.
func (t *Tokens) readDue(ctx context.Context, holder *garam.TokenHolder) error {
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := t.client.Get(ctx, t.credentialsKey(holder.Agent), secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get the credential of %s: %w", holder.Agent, err)
	}
	holder.Version = secret.ResourceVersion
	if due, ok := secret.Annotations[agentname.TokensRefreshAnnotation]; ok {
		// A value this operator did not write is due at once: the next write
		// replaces it.
		holder.Due, _ = time.Parse(time.RFC3339, due)
	}
	return nil
}

// Store implements garam.TokenStore. An existing Secret is patched under the
// version Holders read, so a change since refuses the write; an absent one is
// created, owned by the agent's Agent.
func (t *Tokens) Store(ctx context.Context, holder garam.TokenHolder, tokens garam.Tokens, due time.Time) error {
	agent := string(holder.Agent)
	annotations := map[string]string{agentname.TokensRefreshAnnotation: due.UTC().Format(time.RFC3339)}
	if holder.Version == "" {
		owner := &agentv1alpha1.Agent{}
		if err := t.client.Get(ctx, client.ObjectKey{Namespace: t.namespace, Name: agentname.Agent(agent)}, owner); err != nil {
			return fmt.Errorf("get the agent of %s: %w", agent, err)
		}
		key := t.credentialsKey(holder.Agent)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Annotations: annotations},
			Type:       corev1.SecretTypeOpaque,
			Data:       tokenData(tokens),
		}
		if err := controllerutil.SetControllerReference(owner, secret, t.scheme); err != nil {
			return fmt.Errorf("own the credential of %s: %w", agent, err)
		}
		if err := t.client.Create(ctx, secret); err != nil {
			return fmt.Errorf("create the credential of %s: %w", agent, err)
		}
		return nil
	}
	// The version in the patch is a precondition the API server checks: a
	// recovery finalized since the read removed the refresh time, and these
	// tokens may be the old lineage's.
	patch, err := json.Marshal(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{ResourceVersion: holder.Version, Annotations: annotations},
		Data:       tokenData(tokens),
	})
	if err != nil {
		return fmt.Errorf("render the tokens of %s: %w", agent, err)
	}
	key := t.credentialsKey(holder.Agent)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := t.client.Patch(ctx, secret, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("write the tokens of %s: %w", agent, err)
	}
	return nil
}

// credentialsKey names the Secret an agent's adapter mounts its tokens from.
func (t *Tokens) credentialsKey(agent garam.GRN) client.ObjectKey {
	return client.ObjectKey{Namespace: t.namespace, Name: agentname.CredentialsSecret(string(agent))}
}

// tokenData is the credential Secret's data for tokens: each token under the
// key its adapter's file is named for, and the garam server root the mint
// answered beside them (ADR 0073).
func tokenData(tokens garam.Tokens) map[string][]byte {
	data := map[string][]byte{
		garam.TokenKey:      []byte(tokens.Garam.Token),
		garam.ServerRootKey: tokens.Garam.ServerRootPEM,
	}
	if tokens.Control != nil {
		data[garam.ControlTokenKey] = []byte(tokens.Control.Token)
	}
	return data
}
