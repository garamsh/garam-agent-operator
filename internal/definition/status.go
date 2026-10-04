package definition

import "context"

// RecordStatus records a controller's report of an agent: the latest revision it observed and the
// latest it rendered. Each is raised and never lowered, so a report arriving late changes nothing.
func (s *service) RecordStatus(ctx context.Context, agent GRN, observed, rendered Revision) (Status, error) {
	latest, err := s.repository.GetDefinition(ctx, agent)
	if err != nil {
		return Status{}, err
	}
	for _, r := range []Revision{observed, rendered} {
		if r < 1 || r > latest.Revision {
			return Status{}, ErrInvalidStatus
		}
	}
	return s.repository.RecordStatus(ctx, agent, Status{Observed: observed, Rendered: rendered})
}
