package distribution

import "context"

// Prover asks garam to prove, for one decision, the authority of the controller that presented
// token, a garam token minted for this service, and the placement of agent on it when agent is not
// empty. It returns ErrNotProved or ErrUndecided for the answers that refuse; any other error is
// the call's own. A proof is never reused across decisions.
type Prover interface {
	Prove(ctx context.Context, controller, token, agent string) (Proof, error)
}
