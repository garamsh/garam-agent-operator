package definition

import "context"

// Desired returns what the controller operator is owed after a position. A revision recorded for
// no assignment, or for another controller, is never in it.
func (s *service) Desired(ctx context.Context, operator string, after Position) (DesiredPage, error) {
	return s.repository.Desired(ctx, operator, after)
}
