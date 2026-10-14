package execution

import "context"

// Garam is what these routes ask garam, each call for one decision, never reused across them. It
// returns ErrUndecided, ErrNotAuthorized, ErrCredentialRefused or ErrActivationSuperseded for
// the answers that refuse, and a *GaramContractError for an answer under a contract these routes
// do not take; any other error is the call's own.
type Garam interface {
	// Introspect asks introspectAgentExecution about the agent's token, and about generation where
	// it is not empty.
	Introspect(ctx context.Context, agent string, token AgentToken, generation string) (Introspection, error)
	// Activate asks activateAgent, and reports whether garam created the activation (201) rather
	// than answered a replay of it (200).
	Activate(ctx context.Context, agent string, call ActivationCall) (Activation, bool, error)
}
