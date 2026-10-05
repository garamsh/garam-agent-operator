// Package renderer writes the control service's desired state for an agent
// into the Agent this operator builds for it.
package renderer

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// fieldOwnerName is the field manager every write the renderer makes to an Agent is
// recorded under, so an Agent's managedFields show which fields it owns: never
// spec.suspended, which is a person's (ADR 0046).
const fieldOwnerName = "garam-operator-renderer"

var fieldOwner = client.FieldOwner(fieldOwnerName)

// Agent renders revisions into Agents in one namespace, the one the manager
// runs in.
type Agent struct {
	client    client.Client
	namespace string
	image     string
}

// NewAgent returns an Agent rendering into namespace, giving every agent image
// to run: the image is this operator's configuration, never a revision's
// (ADR 0007).
func NewAgent(c client.Client, namespace, image string) *Agent {
	return &Agent{client: c, namespace: namespace, image: image}
}

// Render implements desired.Renderer. Every field a revision decides is written
// on every new revision, so a changed tool set, model, ego or profile reaches
// the agent rather than only the first one (ADR 0043). Identity and names stay
// what the GRN derives (ADR 0032).
func (a *Agent) Render(ctx context.Context, agent desired.Agent) error {
	spec, err := a.specOf(agent)
	if err != nil {
		return err
	}

	existing := &agentv1alpha1.Agent{}
	err = a.client.Get(ctx, client.ObjectKey{Namespace: a.namespace, Name: agentname.Agent(agent.GRN)}, existing)
	if apierrors.IsNotFound(err) {
		created := &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(agent.GRN), Namespace: a.namespace},
			Spec:       spec,
		}
		if err := a.client.Create(ctx, created, fieldOwner); err != nil {
			return fmt.Errorf("create the agent for %s: %w", agent.GRN, err)
		}

		return nil
	}
	if err != nil {
		return fmt.Errorf("get the agent for %s: %w", agent.GRN, err)
	}

	// One source holds a GRN. An Agent constructed from a garam definition, or
	// written by a person, is not this source's to write.
	identity := existing.Spec.Identity
	if identity == nil || identity.GRN != agent.GRN || identity.Source != agentv1alpha1.DesiredSourceControl {
		return desired.ErrNotControlSource
	}

	rendered := existing.DeepCopy()
	rendered.Spec.Image = spec.Image
	rendered.Spec.StorageSize = spec.StorageSize
	rendered.Spec.StorageClassName = spec.StorageClassName
	rendered.Spec.Resources = spec.Resources
	rendered.Spec.Tools = spec.Tools
	rendered.Spec.Model = spec.Model
	rendered.Spec.Ego = spec.Ego
	rendered.Spec.Identity.AssignmentEpoch = spec.Identity.AssignmentEpoch
	if equality.Semantic.DeepEqual(existing.Spec, rendered.Spec) {
		return nil
	}
	// A merge patch names only the fields this writer decides, so a status
	// written meanwhile does not refuse it.
	if err := a.client.Patch(ctx, rendered, client.MergeFrom(existing), fieldOwner); err != nil {
		return fmt.Errorf("render revision %s into the agent for %s: %w", agent.Revision, agent.GRN, err)
	}

	return nil
}

// specOf is the spec a revision renders to, or desired.ErrMalformed where the
// revision cannot be rendered as it stands.
func (a *Agent) specOf(agent desired.Agent) (agentv1alpha1.AgentSpec, error) {
	if agent.GRN == "" {
		return agentv1alpha1.AgentSpec{}, fmt.Errorf("%w: an agent with no GRN", desired.ErrMalformed)
	}
	storageSize, err := resource.ParseQuantity(agent.Profile.StorageSize)
	if err != nil {
		return agentv1alpha1.AgentSpec{}, fmt.Errorf("%w: storage size %q: %v", desired.ErrMalformed, agent.Profile.StorageSize, err)
	}
	model, err := modelOf(agent.Configuration.Model)
	if err != nil {
		return agentv1alpha1.AgentSpec{}, err
	}

	spec := agentv1alpha1.AgentSpec{
		Image:                 a.image,
		CredentialsSecretName: agentname.CredentialsSecret(agent.GRN),
		StorageSize:           storageSize,
		StorageClassName:      agent.Profile.StorageClassName,
		Resources:             agent.Profile.Resources,
		Model:                 model,
		Ego:                   agent.Configuration.Ego,
		Identity: &agentv1alpha1.AgentIdentity{
			GRN: agent.GRN, AssignmentEpoch: agent.Epoch, Source: agentv1alpha1.DesiredSourceControl,
		},
	}
	// An empty pin set is no pin set: the API refuses an empty one, which
	// sherlock refuses to start under.
	if len(agent.Configuration.Tools) > 0 {
		spec.Tools = agentv1alpha1.ToolSet{Pins: agent.Configuration.Tools}
	}

	return spec, nil
}

// modelOf is the model a revision configures, nil where it configures none. Its
// key reference is "<secret-name>/<key>" in the Agent's namespace, each part as
// Kubernetes allows it: the name a DNS subdomain and the key [-._a-zA-Z0-9]+ and
// neither "." nor ".." (k8s.io/apimachinery@v0.36.0 pkg/util/validation
// IsDNS1123Subdomain and IsConfigMapKey). The control service applies the same
// rule to what it stores (its definition.SecretRef); the manager imports nothing
// of that binary, so each side states the rule itself.
func modelOf(model desired.Model) (*agentv1alpha1.ModelSpec, error) {
	if model == (desired.Model{}) {
		return nil, nil
	}
	if model.Provider == "" || model.BaseURL == "" || model.Name == "" {
		return nil, fmt.Errorf("%w: a model missing its provider, base URL or name", desired.ErrMalformed)
	}
	// A reference with no "/" leaves the key empty, which IsConfigMapKey refuses.
	secret, key, _ := strings.Cut(model.APIKeyRef, "/")
	if len(validation.IsDNS1123Subdomain(secret)) > 0 || len(validation.IsConfigMapKey(key)) > 0 {
		return nil, fmt.Errorf("%w: API key reference %q is not <secret-name>/<key>", desired.ErrMalformed, model.APIKeyRef)
	}

	return &agentv1alpha1.ModelSpec{
		Provider: model.Provider, BaseURL: model.BaseURL, Name: model.Name,
		APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: secret, Key: key},
	}, nil
}
