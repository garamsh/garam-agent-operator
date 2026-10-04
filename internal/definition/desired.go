package definition

import "context"

// Position returns the position the latest stored revision took. It moves whenever any revision
// is stored, so a controller waits on it rather than on its own revisions.
func (s *service) Position(ctx context.Context) (Position, error) {
	return s.repository.Position(ctx)
}

// Desired returns the controller operator's whole candidate set, at most limit agents: the latest
// revision of each agent recorded for it. A revision recorded for no assignment, or for another
// controller, is never in it.
func (s *service) Desired(ctx context.Context, operator string, limit int) (DesiredPage, error) {
	return s.repository.Desired(ctx, operator, limit)
}
