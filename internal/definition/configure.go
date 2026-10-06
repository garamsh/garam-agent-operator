package definition

import (
	"context"
	"errors"
	"fmt"
)

// Configure stores the request's configuration as the agent's next revision when the
// expected revision is still the latest. The agent and the profile are resolved in the
// request's organization only. A repeated request key returns the first
// request's outcome; one carrying another binding or agent is refused with ErrRequestReused.
func (s *service) Configure(ctx context.Context, in ConfigureInput) (Applied, error) {
	if err := in.Config.check(); err != nil {
		return Applied{}, err
	}
	if _, err := s.repository.GetProfile(ctx, in.Request.Organization, in.Profile); err != nil {
		return Applied{}, fmt.Errorf("profile %s version %d: %w", in.Profile.Name, in.Profile.Version, err)
	}
	if err := s.cutoverSwitchedOrNone(ctx, in.Agent); err != nil {
		return Applied{}, err
	}
	if err := s.embeddingKept(ctx, in); err != nil {
		return Applied{}, err
	}
	request := Request{Key: in.Request, Binding: in.Binding, Agent: in.Agent}
	assignment := in.Binding.Assignment
	d := Definition{
		Agent:        in.Agent,
		Organization: in.Request.Organization,
		Revision:     in.ExpectedRevision + 1,
		Profile:      in.Profile,
		Config:       in.Config,
		Assignment:   &assignment,
	}
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

// embeddingKept refuses, with ErrEmbeddingImmutable, a request based on the agent's latest
// revision that changes the base URL or name of the embedding that revision names, or removes it.
// A request based on an earlier revision is left to be stored as stale, and one for an agent with
// no revision in the request's organization to be refused as not found.
func (s *service) embeddingKept(ctx context.Context, in ConfigureInput) error {
	latest, err := s.repository.GetDefinition(ctx, in.Agent)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	was := latest.Config.Model.Embedding
	if latest.Organization != in.Request.Organization || latest.Revision != in.ExpectedRevision || was == nil {
		return nil
	}
	if !in.Config.Model.Embedding.sameEndpoint(*was) {
		return ErrEmbeddingImmutable
	}

	return nil
}
