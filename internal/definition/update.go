package definition

import (
	"context"
	"fmt"
)

func (s *service) UpdateDefinition(ctx context.Context, in UpdateInput) (Definition, error) {
	if _, err := s.repository.GetProfile(ctx, in.Profile); err != nil {
		return Definition{}, fmt.Errorf("profile %s version %d: %w", in.Profile.Name, in.Profile.Version, err)
	}
	d := Definition{Agent: in.Agent, Revision: in.BasedOn + 1, Profile: in.Profile, Config: in.Config}
	if err := s.repository.AppendDefinition(ctx, d); err != nil {
		return Definition{}, err
	}
	return s.repository.GetDefinition(ctx, in.Agent)
}
