package definition

import (
	"context"
	"fmt"
)

// Configure stores the request's configuration as the agent's next revision when the
// expected revision is still the latest. A repeated request key returns the first
// request's outcome; one carrying another binding or agent is refused with ErrRequestReused.
func (s *service) Configure(ctx context.Context, in ConfigureInput) (Applied, error) {
	if _, err := s.repository.GetProfile(ctx, in.Profile); err != nil {
		return Applied{}, fmt.Errorf("profile %s version %d: %w", in.Profile.Name, in.Profile.Version, err)
	}
	request := Request{Key: in.Request, Binding: in.Binding, Agent: in.Agent}
	d := Definition{Agent: in.Agent, Revision: in.ExpectedRevision + 1, Profile: in.Profile, Config: in.Config}
	stored, err := s.repository.Configure(ctx, request, d)
	if err != nil {
		return Applied{}, err
	}
	if stored.Binding != in.Binding || stored.Agent != in.Agent {
		return Applied{}, ErrRequestReused
	}
	switch outcome := stored.Outcome.(type) {
	case Applied:
		return outcome, nil
	case Stale:
		return Applied{}, ErrStaleRevision
	default:
		return Applied{}, fmt.Errorf("request %s stored with outcome %T", in.Request.RequestID, outcome)
	}
}
