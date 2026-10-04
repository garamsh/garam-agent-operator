package definition

import "context"

func (s *service) GetDefinition(ctx context.Context, agent GRN) (Definition, error) {
	return s.repository.GetDefinition(ctx, agent)
}
