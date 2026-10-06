package definition

import (
	"context"
	"errors"
	"fmt"
)

// CreateAgent creates an agent in garam, assigned to the request's controller, and stores its
// first revision: the template version's configuration under the profile version, recorded for
// that assignment. The template and the profile are resolved in the request's organization only.
// It reports whether this call registered the creation.
//
// A repeated key with another binding, controller, template or profile is refused with
// ErrRequestReused. An identical repeat asks garam again, which rechecks current authority: a
// registered creation is answered while garam answers the same agent and epoch, and refused with
// ErrAssignmentMoved once the agent has moved. A pending one is resumed. A failed one is answered.
func (s *service) CreateAgent(ctx context.Context, in CreateInput) (Creation, bool, error) {
	t, err := s.repository.GetTemplate(ctx, in.Request.Organization, in.Template)
	if err != nil {
		return Creation{}, false, fmt.Errorf("template %s version %d: %w", in.Template.Name, in.Template.Version, err)
	}
	// Before the creation is stored or garam is asked: a configuration the manager could not
	// render is refused rather than copied into revision 1.
	if err := t.Config.check(); err != nil {
		return Creation{}, false, fmt.Errorf("template %s version %d: %w", in.Template.Name, in.Template.Version, err)
	}
	if _, err := s.repository.GetProfile(ctx, in.Request.Organization, in.Profile); err != nil {
		return Creation{}, false, fmt.Errorf("profile %s version %d: %w", in.Profile.Name, in.Profile.Version, err)
	}
	c, err := s.repository.BeginCreation(ctx, Creation{
		Key: in.Request, Binding: in.Binding, Controller: in.Controller,
		Template: in.Template, Profile: in.Profile, Outcome: Pending{},
	})
	if err != nil {
		return Creation{}, false, err
	}
	if c.Binding != in.Binding || c.Controller != in.Controller || c.Template != in.Template || c.Profile != in.Profile {
		return Creation{}, false, ErrRequestReused
	}
	stored, registered := c.Outcome.(Registered)
	if _, failed := c.Outcome.(Failed); failed {
		return c, false, nil
	}

	answer, err := s.registrar.Register(ctx, Registration{
		Request: in.Request, Controller: in.Controller, OperationRef: in.Binding.OperationRef,
	})
	switch {
	case registered && (errors.Is(err, ErrRegistrationConflict) || err == nil && answer != stored):
		return Creation{}, false, ErrAssignmentMoved
	case registered && err != nil:
		return Creation{}, false, err
	case registered:
		return c, false, nil
	case errors.Is(err, ErrRegistrationRefused), errors.Is(err, ErrRegistrationConflict):
		failed, err := s.repository.FailCreation(ctx, in.Request, Failed{
			Reason: err.Error(), Conflict: errors.Is(err, ErrRegistrationConflict),
		})
		return failed, false, err
	case err != nil:
		return Creation{}, false, fmt.Errorf("register agent: %w", err)
	}
	return s.repository.RegisterCreation(ctx, in.Request, Definition{
		Agent:        answer.Agent,
		Organization: in.Request.Organization,
		Revision:     1,
		Profile:      in.Profile,
		Config:       t.Config,
		Assignment:   &Assignment{Operator: in.Controller, Epoch: answer.Epoch},
	})
}
