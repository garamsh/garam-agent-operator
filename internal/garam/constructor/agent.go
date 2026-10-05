// Package constructor builds the agents garam assigned this operator into the
// namespace the manager runs in, and reads back what this operator holds about
// them.
package constructor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// fieldOwnerName is the field manager every write this constructor makes to an
// Agent is recorded under, so an Agent's managedFields show which fields it
// owns: never spec.suspended, which is a person's (ADR 0046).
const fieldOwnerName = "garam-operator-constructor"

var fieldOwner = client.FieldOwner(fieldOwnerName)

// Agent constructs the agents garam assigned this operator, keeps the image it
// wrote for them current, and observes the ones it has constructed.
//
// Everything it puts in an Agent's spec beyond the credential's name is
// configuration this operator was given: the image and the storage size are
// properties of the cluster the agent runs in, which garam has never seen. Being
// the only writer of the image is also what makes correcting one this operator's
// to do.
//
// It reads its own output back rather than a second store holding it: what a
// report to garam carries is the epoch a construction recorded and the readiness
// a reconcile observed, and those sit on one object.
type Agent struct {
	client      client.Client
	scheme      *runtime.Scheme
	namespace   string
	image       string
	storageSize resource.Quantity

	// workspaceStorageSize is nil where the operator names no workspace size,
	// and the workspace is then claimed at storageSize.
	workspaceStorageSize *resource.Quantity
}

// NewAgent returns an Agent constructing into namespace, giving each agent
// image to run, storageSize to keep its state on, and workspaceStorageSize for
// its workspace where it is not nil.
func NewAgent(c client.Client, scheme *runtime.Scheme, namespace, image string,
	storageSize resource.Quantity, workspaceStorageSize *resource.Quantity) *Agent {
	return &Agent{client: c, scheme: scheme, namespace: namespace, image: image,
		storageSize: storageSize, workspaceStorageSize: workspaceStorageSize}
}

// HasCredential reports whether the Secret an agent's workload mounts exists.
// It reads the metadata and nothing else, so no part of this operator holds the
// key material it placed.
func (a *Agent) HasCredential(ctx context.Context, agent garam.GRN) (bool, error) {
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err := a.client.Get(ctx, a.credentialsKey(agent), secret)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get the credentials secret for %s: %w", agent, err)
	}
	return true, nil
}

// Construct creates the Agent definition describes and places credential in the
// Secret its spec names. The Agent comes first so that the Secret is owned by it
// and goes when it goes; until the Secret arrives the Agent reports the workload
// unbuilt, which is the state the reconciler already answers.
func (a *Agent) Construct(ctx context.Context, definition garam.Definition, epoch int64,
	credential garam.AgentCredential) error {
	constructed, err := a.ensureAgent(ctx, definition, epoch)
	if err != nil {
		return err
	}
	return a.placeCredential(ctx, constructed, credential)
}

// ensureAgent creates the Agent definition describes where the namespace does
// not carry one, with the GRN it was constructed from and the epoch garam holds
// it at as its identity, and reports both on it.
//
// The spec it writes is this operator's configuration and the definition's
// declaration, and the two never overlap: what a definition declares is the tool
// set, and what a definition cannot name is the image and the storage size.
func (a *Agent) ensureAgent(ctx context.Context, definition garam.Definition,
	epoch int64) (*agentv1alpha1.Agent, error) {
	agent := definition.Agent
	constructed := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: Name(agent), Namespace: a.namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 a.image,
			CredentialsSecretName: agentname.CredentialsSecret(string(agent)),
			StorageSize:           a.storageSize,
			WorkspaceStorageSize:  a.workspaceStorageSize,
			Tools:                 agentv1alpha1.ToolSet{Pins: definition.Tools.Pins},
			Identity:              identityOf(agent, epoch),
		},
	}

	err := a.client.Create(ctx, constructed, fieldOwner)
	if apierrors.IsAlreadyExists(err) {
		if err := a.client.Get(ctx, client.ObjectKeyFromObject(constructed), constructed); err != nil {
			return nil, fmt.Errorf("get the agent constructed for %s: %w", agent, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("create the agent for %s: %w", agent, err)
	}

	if constructed.Status.Agent == string(agent) && constructed.Status.Epoch == epoch {
		return constructed, nil
	}
	// Reported as well as written in the spec: the spec is what the agent is
	// started under, and the status is what this operator observed and reports
	// to garam, where a claim is durable.
	//
	// The epoch is written here and read everywhere else. The caller reaches
	// this only after garam's certificate route answered, and that route answers
	// for an agent assigned to this operator at the current epoch, so this is
	// the one place the value is proved rather than assumed.
	//
	// A merge patch and not an update: the reconciler is woken by the create
	// above and writes the same status, and an update refused for the version
	// it raced would cost the certificate this pass is about to ask for.
	patch, err := json.Marshal(map[string]any{
		"status": map[string]any{"agent": string(agent), "epoch": epoch},
	})
	if err != nil {
		return nil, fmt.Errorf("render the report of %s: %w", agent, err)
	}
	if err := a.client.Status().Patch(ctx, constructed,
		client.RawPatch(types.MergePatchType, patch), client.FieldOwner(fieldOwnerName)); err != nil {
		return nil, fmt.Errorf("report %s on the agent constructed for it: %w", agent, err)
	}
	constructed.Status.Agent = string(agent)
	constructed.Status.Epoch = epoch
	return constructed, nil
}

// placeCredential writes credential into the Secret the Agent's spec names, and
// does nothing where the namespace already carries one: this operator places an
// agent's first credential and replaces none, because an agent renews its own
// over the connection that credential authenticated.
func (a *Agent) placeCredential(ctx context.Context, constructed *agentv1alpha1.Agent, credential garam.AgentCredential) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      constructed.Spec.CredentialsSecretName,
			Namespace: constructed.Namespace,
		},
		Data: map[string][]byte{
			garam.CertificateKey: credential.CertificatePEM,
			garam.KeyKey:         credential.KeyPEM,
			garam.IssuerKey:      credential.IssuerPEM,
			garam.ServerRootKey:  credential.ServerRootPEM,
		},
	}
	if err := controllerutil.SetControllerReference(constructed, secret, a.scheme); err != nil {
		return fmt.Errorf("own the credentials secret for %s: %w", constructed.Status.Agent, err)
	}

	if err := a.client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("place the credential for %s: %w", constructed.Status.Agent, err)
	}
	return nil
}

// CorrectSpec brings the fields of the Agent constructed for agent that
// construction writes to what they should be, and reports whether any moved:
// the image to the one this operator is configured with, and the identity,
// where the spec carries none, to the GRN and epoch the construction reported.
//
// Construction writes the image from this operator's configuration and nothing
// else can, so an image corrected after an agent was built reaches it by no
// other route: a definition is claimed once, and editing the spec by hand is the
// defect one level down. The identity is the same case met once: an Agent
// constructed before the spec carried one has it only in its status, and
// construction is not reached again for an agent whose credential is placed.
// It is filled and never changed, so no GRN is minted or moved and the names
// derived from it stay as they are.
//
// What it writes is bounded by status.agent naming the same agent. That field
// carries the GRN a construction recorded and is empty on an Agent a user wrote,
// whose spec is theirs.
func (a *Agent) CorrectSpec(ctx context.Context, agent garam.GRN) (bool, error) {
	constructed := &agentv1alpha1.Agent{}
	err := a.client.Get(ctx, client.ObjectKey{Namespace: a.namespace, Name: Name(agent)}, constructed)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get the agent constructed for %s: %w", agent, err)
	}
	if constructed.Status.Agent != string(agent) {
		return false, nil
	}

	corrections := map[string]any{}
	if constructed.Spec.Image != a.image {
		corrections["image"] = a.image
	}
	if constructed.Spec.Identity == nil {
		corrections["identity"] = identityOf(agent, constructed.Status.Epoch)
	}
	if len(corrections) == 0 {
		return false, nil
	}

	// A merge patch and not an update: an update carries the resource version
	// this object was read at, so a status written between the read and the
	// write refuses it. The patch names only the fields this writer decided.
	patch, err := json.Marshal(map[string]any{"spec": corrections})
	if err != nil {
		return false, fmt.Errorf("render the corrections of %s: %w", agent, err)
	}
	if err := a.client.Patch(ctx, constructed,
		client.RawPatch(types.MergePatchType, patch), fieldOwner); err != nil {
		return false, fmt.Errorf("correct the spec of the agent constructed for %s: %w", agent, err)
	}
	return true, nil
}

// identityOf is the identity an agent is started under: its GRN, and the epoch
// garam was proved to hold it at where one is known. Zero is no epoch: garam's
// epochs start at one, and an agent constructed before epochs were recorded
// carries none.
func identityOf(agent garam.GRN, epoch int64) *agentv1alpha1.AgentIdentity {
	// A garam definition is this agent's source: an agent the control service
	// created never reaches the poller (ADR 0043).
	identity := &agentv1alpha1.AgentIdentity{GRN: string(agent), Source: agentv1alpha1.DesiredSourceGaram}
	if epoch > 0 {
		identity.AssignmentEpoch = strconv.FormatInt(epoch, 10)
	}
	return identity
}

// credentialsKey names the Secret an agent's workload mounts its credential
// from.
func (a *Agent) credentialsKey(agent garam.GRN) client.ObjectKey {
	return client.ObjectKey{Namespace: a.namespace, Name: agentname.CredentialsSecret(string(agent))}
}

// Name is what the Agent constructed for a GRN is called (agentname.Agent).
func Name(agent garam.GRN) string {
	return agentname.Agent(string(agent))
}
