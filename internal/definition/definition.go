package definition

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
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

// SecretRef names where a secret is held. It is never the secret itself.
//
// Its form is "<secret-name>/<key>": a Secret, and one of its data keys, in the namespace the
// manager renders the agent into (ADR 0043). The name is a DNS subdomain, as Kubernetes requires
// of a Secret's name (k8s.io/apimachinery@v0.36.0 pkg/util/validation IsDNS1123Subdomain), and the
// key matches [-._a-zA-Z0-9]+ and is neither "." nor "..", as Kubernetes requires of a Secret's
// data key (IsConfigMapKey). Neither can hold a "/", so the split is unambiguous. This is the
// one statement of the form; control.md cites it.
type SecretRef string

// Parts returns the Secret's name and key, or ErrInvalidSecretRef where the reference is not
// "<secret-name>/<key>" with each part one Kubernetes accepts.
func (r SecretRef) Parts() (name, key string, err error) {
	// A reference with no "/" leaves the key empty, which IsConfigMapKey refuses.
	name, key, _ = strings.Cut(string(r), "/")
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: secret name %q: %s", ErrInvalidSecretRef, name, strings.Join(problems, "; "))
	}
	if problems := validation.IsConfigMapKey(key); len(problems) > 0 {
		return "", "", fmt.Errorf("%w: key %q: %s", ErrInvalidSecretRef, key, strings.Join(problems, "; "))
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
}

// Configuration is the desired configuration delivered to the agent.
type Configuration struct {
	Model Model
	Ego   string
	Tools ToolPins
}

// check refuses a configuration the manager could not render: a model whose key reference is
// not what SecretRef states. A configuration naming no model at all names no key.
func (c Configuration) check() error {
	if c.Model == (Model{}) {
		return nil
	}
	_, _, err := c.Model.APIKey.Parts()

	return err
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

	// ErrAssignmentMoved is returned for a repeat of a registered creation whose agent garam no
	// longer holds where the creation assigned it.
	ErrAssignmentMoved = errors.New("the agent's assignment moved since its creation")

	// ErrInvalidSecretRef is returned for a model key reference that is not "<secret-name>/<key>"
	// as SecretRef states it, which the manager could not render.
	ErrInvalidSecretRef = errors.New("model key reference is not <secret-name>/<key>")
)
