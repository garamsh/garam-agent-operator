package definition

import "context"

// Registrar creates an agent in garam, assigned to the registration's controller, and returns the
// GRN garam minted with the epoch of that assignment. Registering one request again returns the
// first registration's answer, after garam rechecks current authority.
// A refusal by current authority wraps ErrRegistrationRefused, and one naming another request or a
// moved agent wraps ErrRegistrationConflict, and an answer garam left undecided wraps
// ErrRegistrationUndecided. Any error leaves a creation garam had not yet answered unknown.
type Registrar interface {
	Register(ctx context.Context, r Registration) (Registered, error)
}
