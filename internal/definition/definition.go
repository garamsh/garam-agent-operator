package definition

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// GRN is the agent's garam resource name, which garam mints at registration.
type GRN string

// Revision numbers an agent's definitions; the first is 1 and each change adds one.
type Revision int64

// Version numbers a template's or a profile's published versions; the first is 1.
type Version int64

// SecretRef names where a secret is held. It is never the secret itself.
type SecretRef string

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

// ProfileRef names one published version of a profile.
type ProfileRef struct {
	Name    string
	Version Version
}

// TemplateRef names one published version of a template.
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

// Profile is a published, immutable version of a named set of execution settings.
type Profile struct {
	Name     string
	Version  Version
	Settings ExecutionSettings
}

// Template is a published, immutable version of a named starting point for an agent.
type Template struct {
	Name    string
	Version Version
	Profile ProfileRef
	Config  Configuration
}

// Definition is one revision of an agent's desired execution definition.
type Definition struct {
	Agent    GRN
	Revision Revision
	Profile  ProfileRef
	Config   Configuration
}

// CreationKey identifies one creation request: a repeat of the same key is the same request.
type CreationKey struct {
	Actor        string
	Organization string
	RequestID    string
}

// Creation records one request to create an agent from a template, and how it ended.
type Creation struct {
	Key      CreationKey
	Template TemplateRef
	Outcome  Outcome
}

// Outcome is where a creation stands: Pending, Registered or Failed.
type Outcome interface {
	outcome()
}

// Pending is a creation garam has not yet answered.
type Pending struct{}

// Registered is a creation garam registered, under the GRN it minted.
type Registered struct {
	Agent GRN
}

// Failed is a creation garam refused.
type Failed struct {
	Reason string
}

func (Pending) outcome()    {}
func (Registered) outcome() {}
func (Failed) outcome()     {}

var (
	// ErrNotFound is returned for a definition, template or profile that does not exist.
	ErrNotFound = errors.New("not found")

	// ErrStaleRevision is returned for an update based on a revision that is no longer the latest.
	ErrStaleRevision = errors.New("stale revision")

	// ErrRequestReused is returned when a creation key arrives again naming a different template.
	ErrRequestReused = errors.New("request id reused for a different request")

	// ErrRegistrationRefused is wrapped by a Registrar whose registration garam refused.
	ErrRegistrationRefused = errors.New("registration refused")
)
