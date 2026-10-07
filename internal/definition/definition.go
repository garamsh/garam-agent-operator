package definition

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/garamsh/garam-agent-operator/internal/secretref"
)

// GRN is the agent's garam resource name, which garam mints at registration.
type GRN string

// Revision numbers an agent's definitions; the first is 1 and each change adds one.
type Revision int64

// String is the revision as every wire carries it: a canonical decimal string.
func (r Revision) String() string {
	return strconv.FormatInt(int64(r), 10)
}

// ParseRevision reads a revision from its canonical decimal string: digits only, no sign, no
// leading zero, at least 1. Anything else is ErrInvalidRevision.
func ParseRevision(s string) (Revision, error) {
	if s == "" || s[0] == '0' || strings.TrimLeft(s, "0123456789") != "" {
		return 0, ErrInvalidRevision
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, ErrInvalidRevision
	}
	return Revision(n), nil
}

// Version numbers a template's or a profile's published versions; the first is 1.
type Version int64

// SecretRef names where a secret is held. It is never the secret itself. Its form is
// "<secret-name>/<key>", in the namespace the manager renders the agent into (ADR 0043), as
// secretref.Parse states it.
type SecretRef string

// Parts returns the Secret's name and key, or ErrInvalidSecretRef where the reference is not
// "<secret-name>/<key>" with each part one Kubernetes accepts.
func (r SecretRef) Parts() (name, key string, err error) {
	if name, key, err = secretref.Parse(string(r)); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidSecretRef, err)
	}

	return name, key, nil
}

// ToolPins maps each tool an agent may load to that tool's pin, which is opaque here.
type ToolPins map[string]string

// Model is the language model an agent calls.
type Model struct {
	Provider string
	BaseURL  string
	Name     string
	APIKey   SecretRef
	// Embedding is the embeddings endpoint the agent's memory is recalled with, nil where the
	// model names none. Every model but the mock needs one (ADR 0052).
	Embedding *Embedding
}

// Embedding is an embeddings endpoint. APIKey is empty where the endpoint takes no key. Once an
// agent's revision names one, its base URL and name never change and it is never removed: the
// agent's stored memory carries the vectors it produced (ADR 0052).
type Embedding struct {
	BaseURL string
	Name    string
	APIKey  SecretRef
}

// mockProvider is the model provider sherlock runs offline, with no embeddings endpoint.
const mockProvider = "mock"

// Configuration is the desired configuration delivered to the agent.
type Configuration struct {
	Model Model
	Ego   string
	Tools ToolPins
}

// check refuses a configuration the manager could not render: a model whose key reference is
// not what SecretRef states, a model other than the mock naming no embedding, or an embedding
// missing its base URL or name or with a malformed key reference. A configuration naming no
// model at all names no key and no embedding.
func (c Configuration) check() error {
	if c.Model == (Model{}) {
		return nil
	}
	if _, _, err := c.Model.APIKey.Parts(); err != nil {
		return err
	}
	e := c.Model.Embedding
	switch {
	case e == nil && c.Model.Provider != mockProvider:
		return fmt.Errorf("%w: model provider %q names no embedding", ErrEmbeddingRequired, c.Model.Provider)
	case e == nil:
		return nil
	case e.BaseURL == "" || e.Name == "":
		return fmt.Errorf("%w: an embedding missing its base URL or name", ErrEmbeddingRequired)
	case e.APIKey != "":
		_, _, err := e.APIKey.Parts()
		return err
	}

	return nil
}

// sameEndpoint reports whether e names the embeddings endpoint was set to: the same base URL and
// name. The key it is reached with may differ.
func (e *Embedding) sameEndpoint(was Embedding) bool {
	return e != nil && e.BaseURL == was.BaseURL && e.Name == was.Name
}

// ProfileRef names one published version of a profile, within an organization the caller states
// beside it.
type ProfileRef struct {
	Name    string
	Version Version
}

// TemplateRef names one published version of a template, within an organization the caller states
// beside it.
type TemplateRef struct {
	Name    string
	Version Version
}

// ExecutionSettings are the operator-chosen settings an agent's workload runs with.
// The container image is not one of them: it stays the operator's own configuration.
type ExecutionSettings struct {
	Resources        corev1.ResourceRequirements
	StorageSize      resource.Quantity
	StorageClassName *string
	// WorkspaceStorageSize sizes the agent's workspace claim, nil where the profile leaves it to
	// StorageSize (ADR 0044).
	WorkspaceStorageSize *resource.Quantity
}

// check refuses settings an agent's workload could not run with (ADR 0056): a storage size not
// above zero, a workspace size not above zero where one is named, a storage class name that is
// not a DNS subdomain, or a resource request above its own limit.
func (s ExecutionSettings) check() error {
	if s.StorageSize.Sign() <= 0 {
		return fmt.Errorf("%w: storage size %s is not above zero", ErrInvalidProfile, s.StorageSize.String())
	}
	if s.WorkspaceStorageSize != nil && s.WorkspaceStorageSize.Sign() <= 0 {
		return fmt.Errorf("%w: workspace storage size %s is not above zero", ErrInvalidProfile, s.WorkspaceStorageSize.String())
	}
	if s.StorageClassName != nil {
		if problems := validation.IsDNS1123Subdomain(*s.StorageClassName); len(problems) > 0 {
			return fmt.Errorf("%w: storage class %q: %s", ErrInvalidProfile, *s.StorageClassName, strings.Join(problems, "; "))
		}
	}
	for name, request := range s.Resources.Requests {
		if limit, ok := s.Resources.Limits[name]; ok && request.Cmp(limit) > 0 {
			return fmt.Errorf("%w: %s request %s is above its limit %s", ErrInvalidProfile, name, request.String(), limit.String())
		}
	}
	return nil
}

// Same reports whether s and o are one set of settings: equal quantities written in any form, and
// no resource list and an empty one, are the same.
func (s ExecutionSettings) Same(o ExecutionSettings) bool {
	return equality.Semantic.DeepEqual(s, o)
}

// Profile is a published, immutable version of a named set of execution settings. Its name and
// versions are its organization's own: another organization's profile of the same name is another.
type Profile struct {
	Name     string
	Version  Version
	Settings ExecutionSettings
}

// Template is a published, immutable version of a named starting point for an agent. Its name and
// versions are its organization's own, and the profile it names is one of that organization's.
type Template struct {
	Name    string
	Version Version
	Profile ProfileRef
	Config  Configuration
}

// Definition is one revision of an agent's desired execution definition. Every revision of an
// agent belongs to the organization its first did, and names a profile version of that organization.
type Definition struct {
	Agent        GRN
	Organization string
	Revision     Revision
	Profile      ProfileRef
	Config       Configuration
	// Assignment is where the agent ran when this revision was authorized, or nil when
	// none was recorded with it. A controller is released only a revision recorded for it.
	Assignment *Assignment
}

// Position orders every stored revision by when it was stored; a later one has a greater one.
type Position int64

// DesiredRevision is a controller's view of an agent's latest revision: the definition, and
// the settings of the profile version it names.
type DesiredRevision struct {
	Definition Definition
	Settings   ExecutionSettings
	// Cutover is true for an agent whose revisions began with a cutover import garam recorded as
	// switched: the controller takes it over from the source it was built from.
	Cutover bool
	// Stopped is true while a stop holds the agent: the controller keeps its runtime stopped.
	Stopped bool
	// Recovery is the agent's open recovery, nil where none is open: the controller prepares its
	// certificate request.
	Recovery *OpenRecovery
}

// OpenRecovery is what a controller prepares an open recovery's certificate request under.
type OpenRecovery struct {
	RequestID string
	Epoch     string
}

// DesiredPage is a controller's whole candidate set: the latest revision of each agent recorded
// for it, and the position the set was read at.
type DesiredPage struct {
	Position  Position
	Revisions []DesiredRevision
}

// Status is what controllers reported of an agent: the latest revision observed and rendered,
// and the revision the runtime applied, which no controller report sets.
type Status struct {
	Observed Revision
	Rendered Revision
	Applied  *Revision
}

// RequestKey identifies one console request: a repeat of the same key is the same request.
type RequestKey struct {
	Organization string
	RequestID    string
}

// CreateInput is a request to create an agent assigned to a controller, from a template version
// under a profile version.
type CreateInput struct {
	Request    RequestKey
	Binding    Binding
	Controller string
	Template   TemplateRef
	Profile    ProfileRef
}

// Creation records one request to create an agent, and how it ended. A repeat of its key with
// another binding, controller, template or profile is another request.
type Creation struct {
	Key        RequestKey
	Binding    Binding
	Controller string
	Template   TemplateRef
	Profile    ProfileRef
	Outcome    Outcome
}

// Registration is what garam is asked to create an agent under: the request, the controller the
// agent is assigned to, and the operation reference the console's authority carried.
type Registration struct {
	Request      RequestKey
	Controller   string
	OperationRef string
}

// Binding is what garam's operation authority bound a console request to. It is stored
// with the request as data and compared on every repeat; nothing here interprets it.
type Binding struct {
	Actor        string
	Operation    string
	Target       string
	BodySHA256   string
	OperationRef string
	Assignment   Assignment
}

// PublishInput is a request to publish a template's next version.
type PublishInput struct {
	Request  RequestKey
	Binding  Binding
	Template Template
}

// Publication records one publish request and the version it published, which every repeat of
// its key returns.
type Publication struct {
	Key      RequestKey
	Binding  Binding
	Template TemplateRef
}

// Execution is what is known of an agent's execution: the revision its organization asked
// for, the revision a controller reported rendering, and what the running agent reported.
type Execution struct {
	// Desired is the agent's latest revision.
	Desired Revision
	// Rendered is the latest revision a controller reported rendering, 0 before any.
	Rendered Revision
	// Effective is what the running agent reported, nil until a runtime report is accepted. It
	// is never inferred from the other two.
	Effective *Effective
}

// Effective is an accepted runtime report: the revision the agent runs, under which generation,
// and when it said so.
type Effective struct {
	Revision   Revision
	Generation string
	ObservedAt time.Time
}

// Assignment is where an agent ran when its configuration change was authorized.
type Assignment struct {
	Operator string
	Epoch    string
}

// ConfigureInput is a request to change an agent's definition, stating the revision it expects
// to replace.
type ConfigureInput struct {
	Request          RequestKey
	Binding          Binding
	Agent            GRN
	ExpectedRevision Revision
	Profile          ProfileRef
	Config           Configuration
}

// Request records one configure request and its first outcome, which every repeat returns.
type Request struct {
	Key     RequestKey
	Binding Binding
	Agent   GRN
	Outcome RequestOutcome
}

// RequestOutcome is how a configure request ended: Applied or Stale.
type RequestOutcome interface {
	requestOutcome()
}

// Applied is a configure request stored as the agent's revision.
type Applied struct {
	Revision Revision
}

// Stale is a configure request whose expected revision was no longer the latest.
type Stale struct{}

func (Applied) requestOutcome() {}
func (Stale) requestOutcome()   {}

// Outcome is where a creation stands: Pending, Registered or Failed.
type Outcome interface {
	outcome()
}

// Pending is a creation garam has not yet answered.
type Pending struct{}

// Registered is a creation garam registered, under the GRN it minted and the epoch of the
// agent's first assignment.
type Registered struct {
	Agent GRN
	Epoch string
}

// Failed is a creation garam refused. Conflict is a refusal that names another request: another
// reference, controller or request under the same request identifier.
type Failed struct {
	Reason   string
	Conflict bool
}

func (Pending) outcome()    {}
func (Registered) outcome() {}
func (Failed) outcome()     {}

var (
	// ErrNotFound is returned for a definition, template or profile that does not exist in the
	// organization asked about, whether or not another organization holds one under that name.
	ErrNotFound = errors.New("not found")

	// ErrStaleRevision is returned for a configure request expecting a revision that is no longer
	// the latest.
	ErrStaleRevision = errors.New("stale revision")

	// ErrRequestReused is returned when a request key arrives again for a different request:
	// another actor, operation, target, body or template.
	ErrRequestReused = errors.New("request id reused for a different request")

	// ErrInvalidRevision is returned for a revision that is not a canonical decimal string of 1 or more.
	ErrInvalidRevision = errors.New("revision is not a canonical decimal string of 1 or more")

	// ErrInvalidStatus is returned for a status naming a revision the agent does not have.
	ErrInvalidStatus = errors.New("status names a revision the agent does not have")

	// ErrRegistrationRefused is wrapped by a Registrar whose registration current authority refused.
	ErrRegistrationRefused = errors.New("registration refused")

	// ErrRegistrationConflict is wrapped by a Registrar whose registration garam refused as another
	// request, or, for one already registered, because the agent has moved since.
	ErrRegistrationConflict = errors.New("registration conflicts")

	// ErrRegistrationUndecided is wrapped by a Registrar garam did not answer with a decision: it
	// answered 500 or 503 on every attempt, or could not be reached. The creation's outcome is unknown.
	ErrRegistrationUndecided = errors.New("registration undecided")

	// ErrGaramContractUnsupported is wrapped by a Registrar or an Issuer that garam answered under
	// a contract version this service does not take, or under none. Nothing in the answer is read,
	// so the outcome is unknown, as an undecided one is.
	ErrGaramContractUnsupported = errors.New("garam answered under a contract this service does not take")

	// ErrAssignmentMoved is returned for a repeat of a registered creation whose agent garam no
	// longer holds where the creation assigned it.
	ErrAssignmentMoved = errors.New("the agent's assignment moved since its creation")

	// ErrInvalidSecretRef is returned for a model key reference that is not "<secret-name>/<key>"
	// as SecretRef states it, which the manager could not render.
	ErrInvalidSecretRef = errors.New("model key reference is not <secret-name>/<key>")

	// ErrEmbeddingRequired is returned for a model other than the mock naming no embedding, or
	// for an embedding missing its base URL or name: sherlock refuses to start on either (ADR 0052).
	ErrEmbeddingRequired = errors.New("the model needs an embeddings endpoint with a base URL and a name")

	// ErrEmbeddingImmutable is returned for a configure changing the base URL or name of the
	// embedding the agent's latest revision names, or removing it (ADR 0052).
	ErrEmbeddingImmutable = errors.New("an agent's embedding cannot be changed or removed once set")

	// ErrImportOpen is returned for a cutover import of an agent that holds another import.
	ErrImportOpen = errors.New("another cutover import is open for the agent")

	// ErrAlreadyDefined is returned for a cutover import of an agent that already has a definition.
	ErrAlreadyDefined = errors.New("the agent already has a definition here")

	// ErrCutoverPending is returned for a change to an agent whose cutover import is not switched:
	// nothing of it is released, and nothing may be added to it, before garam records the switch.
	ErrCutoverPending = errors.New("the agent's cutover is not switched")

	// ErrCutoverStage is returned for a cutover stage the stored import is not ready for.
	ErrCutoverStage = errors.New("the cutover import is not at a stage this one follows")

	// ErrReverseMigrationRequired is returned for a rollback of a switched cutover: the source
	// cannot return to garam from here.
	ErrReverseMigrationRequired = errors.New("the cutover is switched; only a reverse migration returns it")

	// ErrActivationMismatch is returned when garam answers a stored activation request with
	// another activation than the one recorded for it.
	ErrActivationMismatch = errors.New("garam answered another activation for the request")

	// ErrPlacementSuperseded is returned for a placement registration naming a Pod whose
	// placement was replaced. Nothing revives it: not a repeat, and not a refresh of its leaf.
	ErrPlacementSuperseded = errors.New("the placement was superseded")

	// ErrPreviousMismatch is returned for a placement registration whose previous placement is
	// not the one held for the agent, including one naming none while one is held.
	ErrPreviousMismatch = errors.New("the previous placement is not the one held for the agent")

	// ErrEvidenceMissing is returned for a placement registration replacing the held placement
	// without the digest of its writer-stopped evidence.
	ErrEvidenceMissing = errors.New("the replaced placement carries no writer-stopped evidence")

	// ErrPlacementConflict is returned for a registration of the held placement that differs
	// from how it was registered in anything but the leaf.
	ErrPlacementConflict = errors.New("the placement was registered differently")

	// ErrIssuanceUndecided is wrapped by an Issuer garam did not answer with a decision. The
	// certificate request's outcome is unknown, so it stays pending and is sent again unchanged.
	ErrIssuanceUndecided = errors.New("certificate issuance undecided")

	// ErrCreationArchived is returned for a first certificate asked for an agent whose only creation
	// is one the published 7c216469476d registered, which migration 2 archived without the agent:create
	// reference garam requires, because that release never stored one (ADR 0058). Its message names
	// the route that keeps the agent, credential recovery followed by a configure, proven against
	// garam (ADR 0063), and re-creation, which loses it.
	ErrCreationArchived = errors.New("the agent's creation was archived by the upgrade, with no agent:create " +
		"reference to ask garam under. To keep the agent, its GRN, identity and memory, have an owner or admin " +
		"recover its credential through control's recovery route (POST /v1/orgs/{org}/agents/{agent}/recovery under " +
		"agent:recover, ADR 0057, ADR 0063), which gives it a certificate, and then configure it, because revision 1 " +
		"has no reference to be activated under; or re-create the agent through the console's create route, which " +
		"gives it a new GRN, so its identity and memory do not carry over")

	// ErrInvalidProfile is returned for a profile publication with no name, a version below 1, or
	// settings an agent's workload could not run with (ADR 0056).
	ErrInvalidProfile = errors.New("the profile is not one an agent can run with")

	// ErrProfileVersionConflict is returned for a profile version already published with other
	// settings: a published version is never changed (ADR 0056).
	ErrProfileVersionConflict = errors.New("the profile version is published with other settings")

	// ErrProfileVersionGap is returned for a profile version that is neither published nor the one
	// after the latest, so versions stay numbered from 1 without a gap (ADR 0056).
	ErrProfileVersionGap = errors.New("the profile version is not the next one")

	// ErrRecoveryOpen is returned for a recovery of an agent that holds another open one.
	ErrRecoveryOpen = errors.New("another recovery is open for the agent")

	// ErrRecoveryStage is returned for a recovery step the stored recovery is not at the stage
	// for: a finalize before its certificate request was prepared, or a prepare after it ended.
	ErrRecoveryStage = errors.New("the recovery is not at a stage this step follows")

	// ErrRecoveryMismatch is returned for a finalize whose body is not the prepared request's
	// bytes, which are the bytes garam's handoff must bind.
	ErrRecoveryMismatch = errors.New("the body is not the prepared recovery request")

	// ErrRecoveryEpoch is returned for a prepared certificate request under another epoch than
	// the one the recovery was opened under.
	ErrRecoveryEpoch = errors.New("the certificate request names another epoch than the recovery")

	// ErrAgentStopped is returned for an activation, or another stop, of an agent a stop holds.
	ErrAgentStopped = errors.New("the agent is stopped")

	// ErrAgentNotStopped is returned for a start of an agent no stop holds.
	ErrAgentNotStopped = errors.New("the agent is not stopped")
)

// CertificateRequest is a controller's request for an agent's first certificate, as it was sent:
// the request id, the assignment epoch it expects, and a PKCS#10 PEM over a key the controller
// holds. Two requests are the same request only when all three are equal.
type CertificateRequest struct {
	RequestID string
	Epoch     string
	CSRPEM    string
}

// IssuedCertificate is the public result garam answered for a certificate request. It carries no
// private key.
type IssuedCertificate struct {
	CertificatePEM string
	IssuerPEM      string
	ServerRootPEM  string
	NotAfter       time.Time
}

// InitialCertificateInput asks for agent's first certificate on behalf of controller.
type InitialCertificateInput struct {
	Agent      GRN
	Controller string
	Request    CertificateRequest
}

// InitialCertificate is the one first-certificate request stored for an agent. Issued is nil while
// the outcome is unknown.
type InitialCertificate struct {
	Agent   GRN
	Request CertificateRequest
	Issued  *IssuedCertificate
}

// Issuance is what an Issuer sends garam: the request, under the reference of the agent's own creation.
type Issuance struct {
	Agent        GRN
	Request      CertificateRequest
	OperationRef string
}

// Refusal is the class of a garam refusal of an issuance, which decides how it is answered.
type Refusal int

const (
	// RefusalForbidden is garam's 403 or 404: current authority does not permit the issuance.
	RefusalForbidden Refusal = iota + 1
	// RefusalConflict is garam's 409: another request is recorded, the epoch is stale, or the
	// lineage was replaced.
	RefusalConflict
	// RefusalInvalid is garam's 422: the certificate request is not one garam signs over.
	RefusalInvalid
)

// IssuanceRefusedError is an Issuer's definite refusal by garam, which records nothing for it.
// Kind and Message are garam's own.
type IssuanceRefusedError struct {
	Refusal Refusal
	Kind    string
	Message string
}

func (e *IssuanceRefusedError) Error() string {
	return "garam refused the certificate request: " + e.Kind + ": " + e.Message
}

// PlacementRequest is a placement as a controller registers it: the Pod, the state claim it
// started on, the assignment epoch, the digest of the Pod's placement token, and the placement it
// replaces. Two registrations are of the same placement only when every field is equal.
type PlacementRequest struct {
	PodUID      string
	PVCUID      string
	Epoch       string
	TokenSHA256 string
	// Previous is the zero value for an agent's first placement.
	Previous PreviousPlacement
}

// PreviousPlacement is the placement a registration replaces, and the digest of the evidence that
// its writers stopped.
type PreviousPlacement struct {
	PodUID              string
	WriterStoppedSHA256 string
}

// PlacementInput registers a placement of agent by controller, whose leaf certificate in that
// handshake was LeafDER.
type PlacementInput struct {
	Agent      GRN
	Controller string
	Request    PlacementRequest
	LeafDER    []byte
}

// Placement is a stored placement. One per agent is current; every one it replaced is kept,
// superseded, so that none is registered again.
type Placement struct {
	Agent      GRN
	Controller string
	Request    PlacementRequest
	LeafDER    []byte
	Superseded bool
}

// PlacementAction is what a registration does to the stored placements.
type PlacementAction int

const (
	// PlacementRepeat answers the current placement as stored.
	PlacementRepeat PlacementAction = iota + 1
	// PlacementRefresh replaces the current placement's leaf, and nothing else of it.
	PlacementRefresh
	// PlacementRegister supersedes the current placement, if any, and makes this one current.
	PlacementRegister
)

// DecidePlacement decides a registration against the agent's current placement and the stored
// placement of the same Pod, either nil where there is none. Both stores apply it inside the one
// step that reads what it is given, so a registration is decided against what it changes.
func DecidePlacement(current, same *Placement, in PlacementInput) (PlacementAction, error) {
	if same != nil {
		if same.Superseded {
			return 0, ErrPlacementSuperseded
		}
		if same.Request != in.Request || same.Controller != in.Controller {
			return 0, ErrPlacementConflict
		}
		if bytes.Equal(same.LeafDER, in.LeafDER) {
			return PlacementRepeat, nil
		}
		return PlacementRefresh, nil
	}
	previous := in.Request.Previous
	if previous == (PreviousPlacement{}) {
		if current != nil {
			return 0, ErrPreviousMismatch
		}
		return PlacementRegister, nil
	}
	if current == nil || current.Request.PodUID != previous.PodUID {
		return 0, ErrPreviousMismatch
	}
	if previous.WriterStoppedSHA256 == "" {
		return 0, ErrEvidenceMissing
	}
	return PlacementRegister, nil
}

// ActivationRequest is an activation as the agent's adapter asks for it, on the placement it was
// asked on. The adapter derives RequestID from the rest, so one intent has one identifier;
// two requests under one identifier are the same request only when every field is equal.
type ActivationRequest struct {
	RequestID       string
	Epoch           string
	Generation      string
	ConfigRevision  Revision
	PlacementPodUID string
}

// Activation is a stored activation request with what it sends garam. ReplacesActivationID and
// OperationRef are fixed when it is first stored, so every attempt sends garam the same request;
// either is empty where garam is sent null. ActivationID is empty until garam answers.
type Activation struct {
	Agent                GRN
	Request              ActivationRequest
	ReplacesActivationID string
	OperationRef         string
	ActivationID         string
}

// RuntimeApplied is the revision the agent's runtime last reported effective, the activation and
// the generation it reported it under, and when it said so.
type RuntimeApplied struct {
	Revision     Revision
	ActivationID string
	Generation   string
	ObservedAt   time.Time
}

// CutoverStage is how far a cutover import has gone.
type CutoverStage string

const (
	// CutoverImported is an import read from garam and stored inactive.
	CutoverImported CutoverStage = "imported"
	// CutoverFrozen is an import garam froze, verified against its digest.
	CutoverFrozen CutoverStage = "frozen"
	// CutoverSwitched is an import garam recorded as switched: its revision 1 is active.
	CutoverSwitched CutoverStage = "switched"
)

// Disposition is what a cutover import does with one of the source's values.
type Disposition string

const (
	// DispositionImport puts a tools.pins.<tool> value into revision 1's tools.
	DispositionImport Disposition = "import"
	// DispositionArchive keeps a value verbatim and applies it nowhere.
	DispositionArchive Disposition = "archive"
	// DispositionNeverApplied is a value the legacy path never applied, archived as such.
	DispositionNeverApplied Disposition = "never-applied"
)

// CutoverImport is a legacy agent's source as control imported it from garam: keyed by its GRN,
// every value kept verbatim with what was done with it, and the digest garam's freeze verifies.
type CutoverImport struct {
	Agent GRN
	// Organization is the one the import was requested in, where its profile is resolved.
	Organization string
	ImportID     string
	Epoch        string
	Assignee     string
	SourceDigest string
	Values       map[string]string
	Dispositions map[string]Disposition
	Profile      ProfileRef
	Pins         ToolPins
	Stage        CutoverStage
	// ConfigureRef is the durable agent:configure reference the switch carried, which revision 1's
	// first activation is sent under.
	ConfigureRef string
}

// RecoveryStage is how far an agent's credential recovery has gone (ADR 0057).
type RecoveryStage string

const (
	// RecoveryRequested is a recovery opened from the console, whose certificate request the
	// agent's controller has not prepared.
	RecoveryRequested RecoveryStage = "requested"
	// RecoveryPrepared is a recovery whose request to garam is stored, as the exact bytes an
	// administrator's agent:recover handoff is minted over.
	RecoveryPrepared RecoveryStage = "prepared"
	// RecoveryFinalized is a recovery garam answered with the recovered credential.
	RecoveryFinalized RecoveryStage = "finalized"
)

// Recovery is one credential recovery of an agent: the console request that opened it, the epoch
// it was opened under, and, once prepared, the request garam is sent, kept as the exact bytes it
// is sent as. One per agent is open, before it is finalized.
type Recovery struct {
	Agent GRN
	// RequestID is the recovery's request identifier on garam: the one its certificate request
	// and its finalize are sent under.
	RequestID string
	// Key and Binding are the console request that opened it, under an agent:recover authority
	// of its own request identifier.
	Key     RequestKey
	Binding Binding
	Epoch   string
	Stage   RecoveryStage
	// Body is the request garam is sent, nil until prepared: {requestId, epoch,
	// certificateRequestPem} as RecoveryBody encodes it.
	Body []byte
	// Recovered is garam's answer, nil until finalized.
	Recovered *RecoveredCredential
}

// RecoveredCredential is what garam answered a recovery with: the new lineage, the certificate
// signed over the prepared request's key, which never left its holder, and the issuer and garam
// server root the signer produced beside it (garam@f54b9e8, ADR-0091). IssuerPEM and ServerRootPEM
// are empty for a recovery a garam before that answered (ADR 0062).
type RecoveredCredential struct {
	Lineage        string
	CertificatePEM string
	IssuerPEM      string
	ServerRootPEM  string
}

// OpenRecoveryInput opens a recovery of Agent, recorded under the console request's key and the
// authority that bound it.
type OpenRecoveryInput struct {
	Key       RequestKey
	Binding   Binding
	Agent     GRN
	RequestID string
}

// Stop is one stop of an agent without a replacement: the console request that made it, the
// activation that was the agent's latest when it was recorded, and whether garam has answered
// that activation's deactivation. It is current until a start ends it, and kept after.
type Stop struct {
	Agent   GRN
	Key     RequestKey
	Binding Binding
	// ActivationID is the agent's latest activation when the stop was recorded, empty where it
	// had none and there was nothing to deactivate.
	ActivationID string
	Deactivated  bool
	// Start is the console request that ended the stop, nil while it is current.
	Start *StopEnd
}

// StopEnd is the console request that ended a stop.
type StopEnd struct {
	Key     RequestKey
	Binding Binding
}

// StopInput stops Agent, or ends its stop, under the console request's key and the authority that
// bound it.
type StopInput struct {
	Key     RequestKey
	Binding Binding
	Agent   GRN
}
