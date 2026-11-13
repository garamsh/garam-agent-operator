package agent

import (
	"context"

	"github.com/garamsh/garam/lib/grn"
)

// Garam is garam's machine listener as the operator uses it, after the
// client-assertion exchange. It is declared here, by the side that
// needs it; the wire client generated from platform/api/machine.yaml is
// not built yet.
type Garam interface {
	// MintTokens mints the agent's tokens as its assignee
	// (mintAgentToken). It is called once for an agent whose Secret is
	// missing, never for one whose Secret exists.
	MintTokens(ctx context.Context, g grn.GRN, epoch int64) (Credentials, error)
	// ReportState reports a provisioning state for g at epoch
	// (reportProvisioningState). A report garam refuses as stale returns
	// ErrStaleEpoch.
	ReportState(ctx context.Context, g grn.GRN, epoch int64, s State) error
}
