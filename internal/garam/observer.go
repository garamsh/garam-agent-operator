package garam

import "context"

// Observer reads back what this operator holds about the agents it constructed,
// so that garam can be told what it sees of their pods.
//
// It is a second view of the objects a Constructor writes rather than a second
// store. The epoch a construction recorded and the readiness a reconcile
// observed sit on one object, so one read answers both and neither writer has
// to hand anything to the reporter.
type Observer interface {
	// Observations returns one entry per agent this operator holds a complete
	// record of: the identity garam minted, the epoch it was constructed at,
	// and what its workload was last observed to report.
	//
	// An agent missing either half of that record is left out rather than
	// reported at a guess. That covers an Agent a user wrote, which names no
	// garam agent, and one constructed before this operator recorded epochs,
	// whose epoch is not obtainable again.
	Observations(ctx context.Context) ([]Observation, error)
}
