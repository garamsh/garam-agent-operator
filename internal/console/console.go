package console

import (
	"errors"
	"time"
)

// Authority is the opaque operation authority the console presents. It is never logged.
type Authority string

const (
	// OperationConfigure is the operation an authority binds to change an agent's definition.
	OperationConfigure = "agent:configure"
	// OperationCreate is the operation an authority binds to create an agent on a controller.
	OperationCreate = "agent:create"
	// OperationCutover is the operation an authority binds to carry one stage of a legacy
	// agent's cutover to garam's route for that stage (garam@1a5273d, ADR-0086).
	OperationCutover = "agent:cutover"
	// The console's reads and template publication, one route each, each binding the exact
	// request target it is sent to (garam@33b1c41 api/machine.yaml OperationBinding).
	OperationTemplateRead    = "agent-template:read"
	OperationTemplatePublish = "agent-template:publish"
	OperationProfileRead     = "execution-profile:read"
	OperationExecutionRead   = "agent:execution-read"
)

// Binding is what an authority binds, as garam's introspection answers it.
type Binding struct {
	OperationRef string
	GrantID      string
	Org          string
	Actor        string
	Audience     string
	Operation    string
	Target       string
	Assignment   *Assignment
	RequestID    string
	BodySHA256   string
	// RequestTarget is the exact route the authority may be carried to, where the operation names
	// one: for agent:cutover, garam's own cutover route for one stage; for the reads and the
	// publication, the exact origin-form target the request arrives here with, escaped path and raw
	// query.
	RequestTarget string
	ExpiresAt     time.Time
}

// Assignment is where the target agent ran when garam authorized a configuration change.
type Assignment struct {
	Operator string
	Epoch    string
}

var (
	// ErrNoAuthority is returned for a request that presents no operation authority.
	ErrNoAuthority = errors.New("no operation authority presented")

	// ErrAuthorityUnknown is returned for an authority garam does not know for this caller:
	// unknown, expired, or another audience's.
	ErrAuthorityUnknown = errors.New("operation authority unknown or expired")

	// ErrAuthorityForbidden is returned for an authority garam's current state no longer authorizes.
	ErrAuthorityForbidden = errors.New("operation authority no longer authorized")

	// ErrAuthorityUndecided is returned when garam could not decide the authority from current state.
	ErrAuthorityUndecided = errors.New("operation authority undecided")

	// ErrGaramContractUnsupported is returned when garam answered under a contract version this
	// service does not take, or under none. Nothing in the answer is read, so it authorizes nothing.
	ErrGaramContractUnsupported = errors.New("garam answered under a contract this service does not take")

	// ErrDigestMismatch is returned for a body whose SHA-256 is not the one the authority binds.
	ErrDigestMismatch = errors.New("request body is not the body the authority binds")
)

// fieldRequestID is the bound field a body's request id is compared with.
const fieldRequestID = "request id"

// MismatchError is returned for an authority that binds a field to another value than the
// request carries.
type MismatchError struct {
	Field string
}

func (e *MismatchError) Error() string {
	return "operation authority binds another " + e.Field
}
