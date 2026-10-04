package console

import (
	"errors"
	"time"
)

// Authority is the opaque operation authority the console presents. It is never logged.
type Authority string

// OperationConfigure is the operation an authority binds to change an agent's definition.
const OperationConfigure = "agent:configure"

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
	ExpiresAt    time.Time
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

	// ErrDigestMismatch is returned for a body whose SHA-256 is not the one the authority binds.
	ErrDigestMismatch = errors.New("request body is not the body the authority binds")
)

// MismatchError is returned for an authority that binds a field to another value than the
// request carries.
type MismatchError struct {
	Field string
}

func (e *MismatchError) Error() string {
	return "operation authority binds another " + e.Field
}
