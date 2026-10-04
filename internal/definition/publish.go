package definition

import (
	"context"
	"fmt"
)

// PublishProfile publishes settings as the next version of org's named profile.
func (s *service) PublishProfile(ctx context.Context, org, name string, settings ExecutionSettings) (Profile, error) {
	return s.repository.PublishProfile(ctx, org, name, settings)
}

// PublishTemplate publishes t as the next version of org's template t.Name. The profile it names
// is resolved in org only, so naming another organization's is ErrNotFound.
func (s *service) PublishTemplate(ctx context.Context, org string, t Template) (Template, error) {
	if err := t.Config.check(); err != nil {
		return Template{}, err
	}
	if _, err := s.repository.GetProfile(ctx, org, t.Profile); err != nil {
		return Template{}, fmt.Errorf("profile %s version %d: %w", t.Profile.Name, t.Profile.Version, err)
	}
	return s.repository.PublishTemplate(ctx, org, t)
}
