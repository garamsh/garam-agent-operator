package agent

import (
	"context"

	"github.com/garamsh/garam/lib/grn"
)

// Feed is the control service's controller API (contract C2). It is
// declared here, by the side that needs it; its wire client is not
// built yet.
type Feed interface {
	// Desired returns the whole releasable set. It is level-triggered:
	// every answer holds every agent, so the operator needs no state to
	// start from.
	Desired(ctx context.Context) ([]Desired, error)
	// ReportRendered tells the control service which revision of g the
	// operator rendered (POST /v1/operators/self/agents/{agent}/status).
	ReportRendered(ctx context.Context, g grn.GRN, revision string) error
}
