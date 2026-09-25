package garam

import "context"

// Constructor builds the agents garam assigned this operator: the Agent the
// reconciler acts on, and the Secret its workload mounts the credential from.
//
// A definition says what an agent is and this operator says how one is built,
// so what a Constructor needs beyond what the definition declares is its own
// configuration: the image and the storage size are properties of a cluster
// garam has never seen.
type Constructor interface {
	// HasCredential reports whether the credential an agent's workload mounts
	// is already placed. garam issues a private key once, so nothing asks for a
	// certificate it has nowhere to put.
	HasCredential(ctx context.Context, agent GRN) (bool, error)

	// Construct creates the Agent a claimed definition declares, records the
	// epoch garam holds the agent at on it, and places credential where that
	// Agent's workload reads it. It is the step that makes the credential
	// obtained: garam keeps no private key and has already moved on, so one this
	// fails to store is recovered by asking for another certificate and never by
	// retrying the write.
	//
	// It takes the whole definition rather than the GRN inside it because the
	// definition now declares something — its tool set — and carries no
	// free-form map for anything else to arrive through. The parameter count
	// falls rather than rises: a Definition carries the GRN.
	//
	// epoch is recorded because a report to garam carries it and nothing else
	// answers it later: a definition's claim reports whichever assignment
	// stands, and a claim is not repeatable.
	Construct(ctx context.Context, definition Definition, epoch int64, credential AgentCredential) error

	// CorrectImage brings the image of an agent this operator already
	// constructed to the one this operator is configured with, and reports
	// whether the field moved. Construction is the only writer of that field,
	// so an operator that wrote it once and left it there leaves a corrected
	// configuration reaching every agent but the ones already built.
	//
	// An Agent this operator did not construct is left alone: its spec is its
	// author's.
	CorrectImage(ctx context.Context, agent GRN) (bool, error)
}
