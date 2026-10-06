// Package renderer writes the control service's desired state for an agent
// into the Agent this operator builds for it.
package renderer

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/secretref"
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
	// written by a person, is not this source's to write — except a garam-source
	// one the control service took over by a cutover, which this render moves to
	// the control source (#217). The move is one way, which the API server
	// enforces (ADR 0043), and it is made in the same patch as the render, so no
	// Agent is ever on the control source with the spec garam's side built.
	identity := existing.Spec.Identity
	cutover := agent.Origin == desired.OriginCutover && identity != nil && identity.GRN == agent.GRN &&
		identity.Source != agentv1alpha1.DesiredSourceControl
	if !cutover && (identity == nil || identity.GRN != agent.GRN || identity.Source != agentv1alpha1.DesiredSourceControl) {
		return desired.ErrNotControlSource
	}

	rendered := existing.DeepCopy()
	rendered.Spec.Identity.Source = agentv1alpha1.DesiredSourceControl
	rendered.Spec.Image = spec.Image
	rendered.Spec.StorageSize = spec.StorageSize
	rendered.Spec.WorkspaceStorageSize = spec.WorkspaceStorageSize
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
	// A closed set (ADR 0050): a value nobody defined is not one to act on.
	if agent.Origin != "" && agent.Origin != desired.OriginCutover {
		return agentv1alpha1.AgentSpec{}, fmt.Errorf("%w: origin %q", desired.ErrMalformed, agent.Origin)
	}
	storageSize, err := resource.ParseQuantity(agent.Profile.StorageSize)
	if err != nil {
		return agentv1alpha1.AgentSpec{}, fmt.Errorf("%w: storage size %q: %v", desired.ErrMalformed, agent.Profile.StorageSize, err)
	}
	workspaceSize, err := workspaceStorageSizeOf(agent.Profile.WorkspaceStorageSize)
	if err != nil {
		return agentv1alpha1.AgentSpec{}, err
	}
	model, err := modelOf(agent.Configuration.Model)
	if err != nil {
		return agentv1alpha1.AgentSpec{}, err
	}

	spec := agentv1alpha1.AgentSpec{
		Image:                 a.image,
		CredentialsSecretName: agentname.CredentialsSecret(agent.GRN),
		StorageSize:           storageSize,
		WorkspaceStorageSize:  workspaceSize,
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
// key reference is "<secret-name>/<key>" in the Agent's namespace, as
// secretref.Parse states it for this binary and the control service both.
func modelOf(model desired.Model) (*agentv1alpha1.ModelSpec, error) {
	if model == (desired.Model{}) {
		return nil, nil
	}
	if model.Provider == "" || model.BaseURL == "" || model.Name == "" {
		return nil, fmt.Errorf("%w: a model missing its provider, base URL or name", desired.ErrMalformed)
	}
	keyRef, err := secretKeyOf(model.APIKeyRef)
	if err != nil {
		return nil, err
	}
	spec := &agentv1alpha1.ModelSpec{
		Provider: model.Provider, BaseURL: model.BaseURL, Name: model.Name, APIKeySecretRef: *keyRef,
	}
	// sherlock starts with no embeddings endpoint on the mock alone (ADR 0052).
	if model.Embedding == nil {
		if model.Provider != mockProvider {
			return nil, fmt.Errorf("%w: a model other than mock with no embedding", desired.ErrMalformed)
		}

		return spec, nil
	}
	if model.Embedding.BaseURL == "" || model.Embedding.Name == "" {
		return nil, fmt.Errorf("%w: an embedding missing its base URL or name", desired.ErrMalformed)
	}
	spec.Embedding = &agentv1alpha1.EmbeddingSpec{BaseURL: model.Embedding.BaseURL, Name: model.Embedding.Name}
	if model.Embedding.APIKeyRef != "" {
		if spec.Embedding.APIKeySecretRef, err = secretKeyOf(model.Embedding.APIKeyRef); err != nil {
			return nil, err
		}
	}

	return spec, nil
}

// mockProvider is the model provider sherlock runs offline, with no embeddings
// endpoint.
const mockProvider = "mock"

// secretKeyOf reads a "<secret-name>/<key>" reference, as secretref.Parse states the form.
func secretKeyOf(ref string) (*agentv1alpha1.SecretKeyReference, error) {
	secret, key, err := secretref.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: API key reference %q is not <secret-name>/<key>", desired.ErrMalformed, ref)
	}

	return &agentv1alpha1.SecretKeyReference{Name: secret, Key: key}, nil
}

// workspaceStorageSizeOf is the workspace claim's size a profile names, nil where it names none,
// so the Agent's workspace claim follows its storage size (ADR 0044). A size that does not parse,
// or is not above zero, leaves the revision unrendered.
func workspaceStorageSizeOf(size *string) (*resource.Quantity, error) {
	if size == nil {
		return nil, nil
	}
	quantity, err := resource.ParseQuantity(*size)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace storage size %q: %v", desired.ErrMalformed, *size, err)
	}
	if quantity.Sign() <= 0 {
		return nil, fmt.Errorf("%w: workspace storage size %q is not above zero", desired.ErrMalformed, *size)
	}

	return &quantity, nil
}
