package garam

import "context"

// Constructor builds the agents garam assigned this operator: the Agent the
// reconciler acts on, and the Secret its adapter mounts the agent's tokens from.
//
// A definition says what an agent is and this operator says how one is built,
// so what a Constructor needs beyond what the definition declares is its own
// configuration: the image and the storage size are properties of a cluster
// garam has never seen.
type Constructor interface {
	// HasCredential reports whether the Secret an agent's adapter mounts its
	// tokens from is already placed. Construction stops there: the refresher
	// keeps the tokens in it current.
	HasCredential(ctx context.Context, agent GRN) (bool, error)

	// Construct creates the Agent a claimed definition declares, records the
	// epoch garam holds the agent at on it, and places token where that Agent's
	// adapter reads it. A token this fails to store is answered by minting
	// another on the next pass, never by retrying the write.
	//
	// It takes the whole definition rather than the GRN inside it because the
	// definition now declares something — its tool set — and carries no
	// free-form map for anything else to arrive through. The parameter count
	// falls rather than rises: a Definition carries the GRN.
	//
	// epoch is recorded because a report to garam carries it and nothing else
	// answers it later: a definition's claim reports whichever assignment
	// stands, and a claim is not repeatable.
	Construct(ctx context.Context, definition Definition, epoch int64, token AgentToken) error

	// CorrectSpec brings the fields construction writes on an agent this
	// operator already constructed to what they should be, and reports whether
	// any moved: the image to the one this operator is configured with, and the
	// identity, where the spec carries none, to the one its status reports.
	// Construction is the only writer of those fields, so an operator that wrote
	// them once and left them there leaves a corrected configuration reaching
	// every agent but the ones already built.
	//
	// An Agent this operator did not construct is left alone: its spec is its
	// author's.
	CorrectSpec(ctx context.Context, agent GRN) (bool, error)

	// HeldByControl reports whether the Agent built for agent takes its desired
	// state from the control service: its spec's source is Control, as a cutover
	// leaves it (#217). Such an agent is not this side's to claim, correct,
	// construct or report, whatever garam lists. It reads the spec, never status.
	HeldByControl(ctx context.Context, agent GRN) (bool, error)
}
