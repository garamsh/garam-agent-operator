package definition

import (
	"context"
	"errors"
	"fmt"
)

// CreateAgent registers an agent with garam and gives it a first revision copied from
// the template. A repeated key returns the first request's outcome, and resumes it
// while garam has not yet answered; one from another actor or naming another template
// is refused with ErrRequestReused.
func (s *service) CreateAgent(ctx context.Context, key RequestKey, actor string, ref TemplateRef) (Creation, error) {
	t, err := s.repository.GetTemplate(ctx, ref)
	if err != nil {
		return Creation{}, fmt.Errorf("template %s version %d: %w", ref.Name, ref.Version, err)
	}
	c, err := s.repository.BeginCreation(ctx, Creation{Key: key, Actor: actor, Template: ref, Outcome: Pending{}})
	if err != nil {
		return Creation{}, err
	}
	if c.Template != ref || c.Actor != actor {
		return Creation{}, ErrRequestReused
	}
	if _, pending := c.Outcome.(Pending); !pending {
		return c, nil
	}

	agent, err := s.registrar.Register(ctx, key)
	if errors.Is(err, ErrRegistrationRefused) {
		return s.repository.FailCreation(ctx, key, err.Error())
	}
	if err != nil {
		return Creation{}, fmt.Errorf("register agent: %w", err)
	}
	return s.repository.RegisterCreation(ctx, key, Definition{
		Agent:    agent,
		Revision: 1,
		Profile:  t.Profile,
		Config:   t.Config,
	})
}
