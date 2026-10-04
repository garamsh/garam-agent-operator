package definition

import (
	"context"
	"fmt"
)

func (s *service) PublishProfile(ctx context.Context, name string, settings ExecutionSettings) (Profile, error) {
	return s.repository.PublishProfile(ctx, name, settings)
}

func (s *service) PublishTemplate(ctx context.Context, t Template) (Template, error) {
	if _, err := s.repository.GetProfile(ctx, t.Profile); err != nil {
		return Template{}, fmt.Errorf("profile %s version %d: %w", t.Profile.Name, t.Profile.Version, err)
	}
	return s.repository.PublishTemplate(ctx, t)
}
