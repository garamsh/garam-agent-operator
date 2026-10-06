package definition

import (
	"context"
	"errors"
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
// creation made is ErrNotFound, as one nobody created is. Effective is the runtime's own accepted
// report, and only while the activation it was made under is the agent's latest, so a generation
// a newer activation replaced is never shown as effective. It is nil until such a report is
// accepted, and never inferred from what a controller rendered.
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
	e := Execution{Desired: d.Revision, Rendered: status.Rendered}
	applied, err := s.repository.GetRuntimeApplied(ctx, agent)
	if errors.Is(err, ErrNotFound) {
		return e, nil
	}
	if err != nil {
		return Execution{}, err
	}
	latest, err := s.repository.LatestActivation(ctx, agent)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Execution{}, err
	}
	if latest != "" && applied.ActivationID == latest {
		e.Effective = &Effective{Revision: applied.Revision, Generation: applied.Generation, ObservedAt: applied.ObservedAt}
	}
	return e, nil
}
