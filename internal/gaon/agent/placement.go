package agent

import (
	"context"

	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// Placement is one placement of an agent as the control service
// registers it: the MicroVM running it and the memory Volume it runs
// on, by the uids gaon gave them, the assignment epoch it runs at, and
// the SHA-256 of the placement token minted for it. The token itself
// is never part of it. It names no `previous`: replacing a placement
// is a later slice (ADR 0154).
type Placement struct {
	Epoch       string
	PodUID      string
	PVCUID      string
	TokenSHA256 string
}

// Placements is the control service's placement registration
// (POST /v1/operators/self/agents/{agent}/placements). It is declared
// here, by the side that needs it.
type Placements interface {
	// RegisterPlacement presents p as the placement of g. It answers
	// whether the control service stored it, and false for one it
	// already held. A refusal for good is ErrPlacementSuperseded, any
	// other refusal ErrPlacementRefused, and any other error is worth
	// presenting again at once.
	RegisterPlacement(ctx context.Context, g grn.GRN, p Placement) (created bool, err error)
}

// ErrPlacementSuperseded is a registration the control service
// refused for good: 409 placement_superseded or epoch_superseded. The
// body is never presented again; a new MicroVM, epoch or token is a
// new body.
var ErrPlacementSuperseded = errorx.New(errorx.KindFailedPrecondition, "placement is superseded")

// ErrPlacementRefused is any other refusal of a registration, which
// the control service may answer differently once what it names
// changes: it is presented again after a wait.
var ErrPlacementRefused = errorx.New(errorx.KindFailedPrecondition, "placement is refused")
