package definition

import "context"

// Registrar registers a new agent with garam and returns the GRN garam minted.
// Registering one key again returns the GRN of the first registration.
// A refusal wraps ErrRegistrationRefused; any other error leaves the outcome unknown.
type Registrar interface {
	Register(ctx context.Context, key RequestKey) (GRN, error)
}
