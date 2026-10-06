package definition

import (
	"context"
	"fmt"
)

// ListTemplates returns the latest version of each of org's templates.
func (s *service) ListTemplates(ctx context.Context, org string) ([]Template, error) {
	return s.repository.ListTemplates(ctx, org)
}

// GetTemplate returns one version of org's template, ErrNotFound for one org never published.
func (s *service) GetTemplate(ctx context.Context, org string, ref TemplateRef) (Template, error) {
	return s.repository.GetTemplate(ctx, org, ref)
}

// ListProfiles returns every version org published of every profile.
func (s *service) ListProfiles(ctx context.Context, org string) ([]ProfileRef, error) {
	return s.repository.ListProfiles(ctx, org)
}

// GetProfile returns one version of org's profile, ErrNotFound for one org never published.
func (s *service) GetProfile(ctx context.Context, org string, ref ProfileRef) (Profile, error) {
	return s.repository.GetProfile(ctx, org, ref)
}

// Execution returns what is known of an agent's execution. An agent another organization's
// creation made is ErrNotFound, as one nobody created is. Effective stays nil: no runtime report
// is accepted yet, and it is never inferred.
func (s *service) Execution(ctx context.Context, org string, agent GRN) (Execution, error) {
	d, err := s.repository.GetDefinition(ctx, agent)
	if err != nil {
		return Execution{}, err
	}
	if d.Organization != org {
		return Execution{}, fmt.Errorf("agent %s: %w", agent, ErrNotFound)
	}
	status, err := s.repository.GetStatus(ctx, agent)
	if err != nil {
		return Execution{}, err
	}
	return Execution{Desired: d.Revision, Rendered: status.Rendered}, nil
}
