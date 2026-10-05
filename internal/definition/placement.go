package definition

import "context"

// RegisterPlacement stores a placement its controller registered, refusing one that would let a
// replaced placement stand again or a placement replace another without its writer-stopped
// evidence. A repeat of the current placement answers it as stored, and refreshes its leaf where
// this one was presented under another.
func (s *service) RegisterPlacement(ctx context.Context, in PlacementInput) (Placement, bool, error) {
	return s.repository.RegisterPlacement(ctx, in)
}

// CurrentPlacement returns the agent's current placement, or ErrNotFound.
func (s *service) CurrentPlacement(ctx context.Context, agent GRN) (Placement, error) {
	return s.repository.CurrentPlacement(ctx, agent)
}
