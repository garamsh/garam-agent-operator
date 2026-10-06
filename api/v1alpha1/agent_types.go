package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AgentSpec defines the desired state of Agent
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.identity) || (has(self.identity) && self.identity.grn == oldSelf.identity.grn)",message="identity.grn cannot be changed or removed once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.identity) || !has(oldSelf.identity.source) || oldSelf.identity.source != 'Control' || (has(self.identity) && has(self.identity.source) && self.identity.source == 'Control')",message="identity.source cannot leave Control once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.model) || !has(oldSelf.model.embedding) || (has(self.model) && has(self.model.embedding) && self.model.embedding.name == oldSelf.model.embedding.name && self.model.embedding.baseURL == oldSelf.model.embedding.baseURL)",message="model.embedding.name and model.embedding.baseURL cannot be changed or removed once set, because the agent's stored memory is embedded under them"
// +kubebuilder:validation:XValidation:rule="!has(self.revision) || (has(self.identity) && has(self.identity.source) && self.identity.source == 'Control')",message="revision is set only on an agent whose identity.source is Control"
// +kubebuilder:validation:XValidation:rule="!has(self.stopped) || !self.stopped || (has(self.identity) && has(self.identity.source) && self.identity.source == 'Control')",message="stopped is set only on an agent whose identity.source is Control"
type AgentSpec struct {
	// type names the agent binary the workload carries. Today three are admitted
	// — sherlock, claude-code and codex — and each maps to a different
	// environment-variable prefix and a different per-type default. Unset
	// defaults to sherlock, which is what every Agent before this field was
	// already.
	//
	// An unknown type is refused at admission; an admitted type that the
	// controller has not yet learned to build is refused on reconcile, with
	// ReasonTypeUnimplemented on Synced.
	// +optional
	// +kubebuilder:validation:Enum=sherlock;claude-code;codex
	Type string `json:"type,omitempty"`

	// image is the container image the agent runs. It has no default: name the
	// image and the tag or digest to run explicitly. A digest names one build and
	// a tag is accepted; what the holder of a tag accepts is that a restart can
	// bring different bytes under the same name, because every container of this
	// Pod is pulled at every start rather than read from a node's cache. An image
	// no registry serves cannot be run here for the same reason, however it
	// reached the node. It must run as uid 65532, which is the user every
	// container of the agent's Pod is given: the agent's credential is delivered
	// as a file that user owns, and the process that reads it is refused a file
	// owned by anyone else. It must also need no Linux capability, no privilege
	// it did not start with, and nothing the runtime's default seccomp profile
	// blocks, because the Pod this operator builds is one PodSecurity restricted
	// admits and grants none of the three.
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// credentialsSecretName is the name of a Secret in the Agent's namespace
	// holding the agent's credential material. The Secret is created outside this
	// operator, and its keys are mounted into the agent as files.
	// +required
	// +kubebuilder:validation:MinLength=1
	CredentialsSecretName string `json:"credentialsSecretName"`

	// storageSize is the size of the persistent volume the agent keeps its state on.
	// +required
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="storageSize must be greater than zero"
	StorageSize resource.Quantity `json:"storageSize"`

	// workspaceStorageSize is the size of the persistent volume the agent's
	// workspace serves its files from, which is a volume of its own: code the
	// agent runs reaches the workspace and never the agent's state. Unset means
	// the size storageSize names.
	// +optional
	// +kubebuilder:validation:XValidation:rule="quantity(string(self)).isGreaterThan(quantity('0'))",message="workspaceStorageSize must be greater than zero"
	WorkspaceStorageSize *resource.Quantity `json:"workspaceStorageSize,omitempty"`

	// suspended stops the agent without deleting anything: its workload is
	// scaled to no replica, and its Pod is released only once its writers are
	// seen to stop. The Agent, its credential and its volumes are kept, and
	// clearing the field starts the agent again on the same volumes. It is a
	// person's to set: this operator never writes it.
	// +optional
	Suspended bool `json:"suspended,omitempty"`

	// stopped is the control service's stop of the agent without a
	// replacement: its workload is scaled to no replica as for suspended, and
	// its Pod is released only once its writers are seen to stop. It is
	// written by this operator from the control service's feed on every
	// render of an agent on the Control source, and cleared when the stop
	// ends. It is set only where identity.source is Control; suspended stays
	// a person's.
	// +optional
	Stopped bool `json:"stopped,omitempty"`

	// storageClassName is the StorageClass the agent's persistent volumes are
	// provisioned from, its state's and its workspace's. Unset means the
	// cluster's default StorageClass.
	// +optional
	// +kubebuilder:validation:MinLength=1
	StorageClassName *string `json:"storageClassName,omitempty"`

	// resources are the compute resources the agent container requests and is
	// limited to. Unset leaves the container without requests or limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitzero"`

	// tools declares the tool set this agent accepts. Unset declares none, and
	// an agent handed no tool set runs its tools unpinned and says so at startup.
	// Declaring one is a statement about the tool tree this operator mounted, so
	// it is delivered to the agent and read nowhere here.
	// +optional
	Tools ToolSet `json:"tools,omitzero"`

	// model is the chat model the agent answers with. Unset leaves the agent on
	// its own default, which for sherlock is a scripted mock that answers
	// nothing. The settings are delivered to the agent in its config file, and
	// the key is delivered from the Secret apiKeySecretRef names, as an
	// environment variable of the agent's container only.
	// +optional
	Model *ModelSpec `json:"model,omitempty"`

	// ego is the organisation's own statement the agent's instructions open
	// with, in place of the default the agent's image embeds. It is the only
	// part of the instructions this field reaches: the agent appends its own
	// contract to it, and that contract is not configurable. Unset leaves the
	// image's default.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Ego string `json:"ego,omitempty"`

	// identity is who the agent is in garam: the GRN garam minted for it and
	// the assignment epoch this operator was proved to hold it at. It is written
	// by this operator when it constructs the agent, and the agent is started
	// under it. Unset on an Agent a user wrote, which is started under its own
	// metadata.name instead: a development identity, not a GRN. The GRN cannot
	// be changed or removed once set.
	// +optional
	Identity *AgentIdentity `json:"identity,omitempty"`

	// revision is the control service's definition revision this spec was
	// rendered from, as the canonical decimal string its feed carries. It is
	// written by this operator on every render of an agent on the Control
	// source, and delivered to the agent in its config file, which the agent
	// reports back as the revision it runs. It is set only where
	// identity.source is Control: an agent on any other source has no
	// definition revision.
	// +optional
	// +kubebuilder:validation:MaxLength=19
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*$`
	Revision string `json:"revision,omitempty"`
}

// AgentIdentity is an agent's identity in garam, as this operator renders it
// into the agent's Pod.
type AgentIdentity struct {
	// grn is the garam resource name of the agent, which the agent serves its
	// routes under. It is opaque here and passed to the agent verbatim.
	// +required
	// +kubebuilder:validation:MinLength=1
	GRN string `json:"grn"`

	// assignmentEpoch is the assignment epoch garam held this agent at when
	// this operator was proved to hold it, opaque and verbatim. Only the writer
	// of identity changes it. Unset means unknown.
	// +optional
	// +kubebuilder:validation:MinLength=1
	AssignmentEpoch string `json:"assignmentEpoch,omitempty"`

	// source is where this agent's desired state comes from. Garam is a garam
	// definition, read once when the agent is constructed. Control is the
	// control service's desired feed, which every new revision is rendered from.
	// Absent is Garam, which is every agent constructed before this field
	// existed. One source holds a GRN at a time, and Control is never left once
	// set.
	// +optional
	// +kubebuilder:validation:Enum=Garam;Control
	Source DesiredSource `json:"source,omitempty"`
}

// DesiredSource names where an agent's desired state comes from.
type DesiredSource string

// The sources an agent's desired state can come from.
const (
	// DesiredSourceGaram is a garam definition.
	DesiredSourceGaram DesiredSource = "Garam"

	// DesiredSourceControl is the control service's desired feed.
	DesiredSourceControl DesiredSource = "Control"
)

// ModelSpec is the chat model an Agent answers with. Every field is required,
// because a field left out would fall back to the agent's own default for it —
// for sherlock, an OpenAI endpoint and model — and a key paired with another
// vendor's endpoint fails at the first request rather than here.
// +kubebuilder:validation:XValidation:rule="self.provider == 'mock' || has(self.embedding)",message="embedding is required with a provider other than mock, because sherlock refuses to start without one"
type ModelSpec struct {
	// provider is the wire protocol the endpoint speaks, in the agent's own
	// words: for sherlock, openai-compatible or anthropic-compatible. It is
	// delivered to the agent and read nowhere here.
	// +required
	// +kubebuilder:validation:MinLength=1
	Provider string `json:"provider"`

	// baseURL is the endpoint's API root, which is what chooses the vendor.
	// +required
	// +kubebuilder:validation:MinLength=1
	BaseURL string `json:"baseURL"`

	// name is the model name passed to the endpoint.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// apiKeySecretRef names the key of a Secret in the Agent's namespace that
	// holds the endpoint's API key. The key is referenced, never carried: this
	// operator does not read it, and the agent's workload is not built until the
	// Secret exists. Agents on one endpoint can name one Secret.
	// +required
	APIKeySecretRef SecretKeyReference `json:"apiKeySecretRef"`

	// embedding is the embeddings endpoint the agent's memory is recalled with.
	// It is required with every provider but mock. Its name and baseURL cannot
	// change once set: the agent's stored memory carries vectors that endpoint
	// produced, and nothing here embeds it again.
	// +optional
	Embedding *EmbeddingSpec `json:"embedding,omitempty"`
}

// EmbeddingSpec is an OpenAI-compatible embeddings endpoint.
type EmbeddingSpec struct {
	// baseURL is the endpoint's API root.
	// +required
	// +kubebuilder:validation:MinLength=1
	BaseURL string `json:"baseURL"`

	// name is the embedding model name passed to the endpoint.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// apiKeySecretRef names the key of a Secret in the Agent's namespace holding
	// the endpoint's API key. Unset sends no key, as a local endpoint needs none.
	// +optional
	APIKeySecretRef *SecretKeyReference `json:"apiKeySecretRef,omitempty"`
}

// SecretKeyReference names one key of a Secret in the referring object's
// namespace.
type SecretKeyReference struct {
	// name is the name of the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key is the key within the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// ToolSet is what an Agent declares about the tools it accepts.
type ToolSet struct {
	// pins name the manifest each tool is accepted at, keyed by the tool's own
	// name. A pin is opaque here: this operator carries it to the agent and
	// never reads it, so what one means and what a wrong one does are the
	// agent's. The set is the whole of what the agent accepts rather than a
	// filter over it — an agent that finds a tool this does not name refuses to
	// start — so a partial set is not a partial statement, while a pin naming a
	// tool the tree does not carry is never consulted. Declaring it empty is
	// refused here rather than delivered: the agent refuses a pin section naming
	// no tool, so an empty one is an agent that cannot start.
	// +optional
	// +kubebuilder:validation:MinProperties=1
	Pins map[string]string `json:"pins,omitempty"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	// conditions report what the controller observed of this Agent. Two types
	// are set, and they answer different questions:
	//
	// - "Synced": True when the cluster carries the workload this Agent's spec
	//   asks for. False when the Secret named by credentialsSecretName or by
	//   model.apiKeySecretRef does not exist, and False when the spec was edited
	//   in a way the running workload cannot take. The reason says which.
	//
	// - "Available": True when the workload reports a ready replica. False when
	//   it reports none, which covers a replica still starting as much as one
	//   that cannot start. Unknown when the controller did not get as far as
	//   reading the workload. A replica is ready once its containers are
	//   running, and this workload carries no readiness probe, so no part of
	//   this says whether the agent inside those containers works.
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the metadata.generation this status was last computed
	// from. A value behind metadata.generation means the status is stale.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// agent is the garam resource name of the agent this Agent was constructed
	// for, and is empty on an Agent a user wrote. garam mints it when an
	// organization defines an agent and this operator is admitted to it by
	// claiming the definition, so it is reported here and never asked for in
	// the spec.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Agent string `json:"agent,omitempty"`

	// epoch is the assignment epoch garam held this agent at when this operator
	// constructed it, and is absent on an Agent a user wrote. It rises with every
	// assignment, and garam accepts a report about an agent only at the epoch the
	// assignment is currently on, so a report this operator sends carries this
	// value and is refused once the agent has been assigned again.
	//
	// It is recorded where garam's certificate route proved it and is never
	// refreshed afterwards. A value re-read later would be whatever epoch the
	// assignment stands at, which is current whoever holds the agent by then, and
	// a report carrying it could never be found stale.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Epoch int64 `json:"epoch,omitempty"`

	// placement is the Pod this Agent's agent last ran in, and the volume claim
	// it ran on, as the controller observed them. A new Pod is a new placement;
	// a container restarted in the same Pod is not. It is a report and is never
	// read back by the controller.
	// +optional
	Placement *Placement `json:"placement,omitempty"`

	// writerStopped is the evidence on which the controller last released a
	// deleting Pod's fence: every container that could write the agent's state
	// had terminated, or the Pod was never scheduled. It is written before the
	// fence is released, and each release replaces it.
	// +optional
	WriterStopped *WriterStoppedEvidence `json:"writerStopped,omitempty"`
}

// Placement is one Pod an agent runs in and the claim of its state volume.
type Placement struct {
	// podUID is the UID of the Pod.
	// +required
	PodUID string `json:"podUID"`

	// pvcUID is the UID of the claim the agent's state volume is bound through,
	// when the Pod was first seen.
	// +optional
	PVCUID string `json:"pvcUID,omitempty"`
}

// WriterStoppedEvidence is what showed that a deleting Pod's writers had
// stopped.
type WriterStoppedEvidence struct {
	// podUID is the UID of the Pod the evidence is about.
	// +required
	PodUID string `json:"podUID"`

	// pvcUID is the UID of the state volume's claim at the time, empty where the
	// Pod was never scheduled.
	// +optional
	PVCUID string `json:"pvcUID,omitempty"`

	// containers are the containers that could write the agent's state, each in
	// its terminated state. Empty where the Pod was never scheduled to a node, so
	// none of them ever started.
	// +optional
	// +listType=atomic
	Containers []TerminatedContainer `json:"containers,omitempty"`

	// observedAt is when the controller read the evidence.
	// +required
	ObservedAt metav1.Time `json:"observedAt"`
}

// TerminatedContainer is one container's terminated state, as the kubelet
// reported it.
type TerminatedContainer struct {
	// name is the container's name.
	// +required
	Name string `json:"name"`

	// containerID is the runtime's ID of the container instance that
	// terminated.
	// +required
	ContainerID string `json:"containerID"`

	// exitCode is the code it exited with.
	// +required
	ExitCode int32 `json:"exitCode"`

	// finishedAt is when it terminated.
	// +optional
	FinishedAt metav1.Time `json:"finishedAt,omitzero"`
}

// ConditionSynced is the condition type reporting whether the cluster carries
// the workload an Agent's spec asks for.
const ConditionSynced = "Synced"

// Reasons for the Synced condition.
const (
	// ReasonWorkloadReconciled is set when the workload matches the spec.
	ReasonWorkloadReconciled = "WorkloadReconciled"

	// ReasonCredentialsSecretMissing is set when the Secret the spec names does
	// not exist, which leaves the workload unbuilt.
	ReasonCredentialsSecretMissing = "CredentialsSecretMissing"

	// ReasonModelKeySecretMissing is set when the Secret the spec names for the
	// model's API key does not exist, which leaves the workload unbuilt.
	ReasonModelKeySecretMissing = "ModelKeySecretMissing"

	// ReasonEmbeddingKeySecretMissing is set when the Secret the spec names for
	// the embeddings endpoint's API key does not exist, which leaves the workload
	// unbuilt.
	ReasonEmbeddingKeySecretMissing = "EmbeddingKeySecretMissing"

	// ReasonWorkloadReplacing is set while the StatefulSet is replaced to give the
	// agent's state and its workspace separate volumes. The old one is deleted
	// leaving its Pod and its claims in place, and the next is created once it
	// is gone.
	ReasonWorkloadReplacing = "WorkloadReplacing"

	// ReasonStorageSizeImmutable is set when the spec asks for a volume size the
	// workload cannot be changed to.
	ReasonStorageSizeImmutable = "StorageSizeImmutable"

	// ReasonStorageClassImmutable is set when the spec asks for a storage class
	// the workload's volume cannot be changed to.
	ReasonStorageClassImmutable = "StorageClassImmutable"

	// ReasonTypeUnimplemented is set when the spec names an admitted type the
	// controller has not yet learned to build. The workload is not built until
	// a controller version that knows the type is deployed.
	ReasonTypeUnimplemented = "TypeUnimplemented"
)

// ConditionAvailable is the condition type reporting whether the workload an
// Agent's spec asks for is running. It is the name a Deployment carries for the
// same split: ConditionSynced reports on the declaration, and this reports on
// what the cluster is running behind it.
const ConditionAvailable = "Available"

// Reasons for the Available condition.
const (
	// ReasonReplicaReady is set when the workload reports a ready replica.
	ReasonReplicaReady = "ReplicaReady"

	// ReasonReplicaNotReady is set when the workload reports no ready replica.
	ReasonReplicaNotReady = "ReplicaNotReady"

	// ReasonWorkloadNotObserved is set when the controller stopped before
	// reconciling a workload, so it read none and observed no readiness.
	ReasonWorkloadNotObserved = "WorkloadNotObserved"

	// ReasonSuspended is set on Available while the spec suspends the agent,
	// which asks for no replica.
	ReasonSuspended = "Suspended"
)

// ConditionSuspended is the condition type reporting whether the agent is
// stopped as its spec asks. True means the spec suspends it and its Pod is
// gone, so its volumes are mounted by nothing: the point at which its state
// can be copied.
const ConditionSuspended = "Suspended"

// Reasons for the Suspended condition. ReasonSuspended, shared with
// Available, is the True one.
const (
	// ReasonSuspending is set while the spec suspends the agent and its Pod
	// still exists: running, or held by its writer fence.
	ReasonSuspending = "Suspending"

	// ReasonNotSuspended is set while the spec does not suspend the agent.
	ReasonNotSuspended = "NotSuspended"
)

// ConditionWriterFence is the condition type reporting the controller's last
// decision on a deleting Pod of this Agent: whether the agent's writers there
// were positively seen to stop. True is released on evidence; Unknown is
// unverified, and the Pod is held until the evidence arrives or a person
// releases it. The fence is never released on a timeout.
const ConditionWriterFence = "WriterFence"

// Reasons for the WriterFence condition.
const (
	// ReasonWriterStopped is set when every writing container was seen
	// terminated, or the Pod was never scheduled, and the evidence was recorded
	// in writerStopped before the Pod was released.
	ReasonWriterStopped = "WriterStopped"

	// ReasonContainerRunning is set when a writing container is still running,
	// which is what a force-deleted Pod looks like until its node reports.
	ReasonContainerRunning = "ContainerRunning"

	// ReasonContainerWaiting is set when a writing container on a scheduled Pod
	// is waiting. A waiting state reported by a node that may be partitioned is
	// not evidence that the container never started.
	ReasonContainerWaiting = "ContainerWaiting"

	// ReasonNoContainerStatus is set when a scheduled Pod reports no status, or
	// no container ID, for a writing container. An absent report can be a
	// started process nobody observed.
	ReasonNoContainerStatus = "NoContainerStatus"

	// ReasonNodeUnknown is set when the Pod's node is gone or its Ready
	// condition is Unknown, so the container states it reported may be stale.
	ReasonNodeUnknown = "NodeUnknown"

	// ReasonPVCChanged is set when the state volume's claim is missing or is
	// not the one the Pod started with.
	ReasonPVCChanged = "PVCChanged"
)

// ConditionStateIsolated is the condition type reporting which shape the
// agent's workload runs in: whether the agent's state and its workspace are on
// separate claims, so the code the agent runs cannot reach its state (ADR 0044).
// False lists an agent still in the shape where the workspace shares the state
// claim, which this operator replaces only where it is told to (ADR 0047).
const ConditionStateIsolated = "StateIsolated"

// Reasons for the StateIsolated condition. WorkloadNotObserved and
// WorkloadReplacing are shared with Synced.
const (
	// ReasonSeparateClaims is set when the workload claims the state and the
	// workspace separately, and only the agent's container mounts the state.
	ReasonSeparateClaims = "SeparateClaims"

	// ReasonSharedClaim is set when the workload is in the shape where the
	// workspace mounts the state claim, and this operator is not told to replace
	// it.
	ReasonSharedClaim = "SharedClaim"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="metadata.name must be 52 characters or fewer, because the Pods of this Agent's workload carry the name with a suffix of up to 11 characters in a label, and a label value stops at 63"
// +kubebuilder:printcolumn:name="Synced",type=string,JSONPath=`.status.conditions[?(@.type=="Synced")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Synced")].reason`
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.conditions[?(@.type=="Available")].status`
// +kubebuilder:printcolumn:name="Isolated",type=string,JSONPath=`.status.conditions[?(@.type=="StateIsolated")].status`
// +kubebuilder:printcolumn:name="Suspended",type=string,JSONPath=`.status.conditions[?(@.type=="Suspended")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is the Schema for the agents API
type Agent struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Agent
	// +required
	Spec AgentSpec `json:"spec"`

	// status defines the observed state of Agent
	// +optional
	Status AgentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Agent{}, &AgentList{})
		return nil
	})
}
