package definition

import (
	"context"
	"errors"
)

// Stop records a stop of an agent of the request's organization, under the agent's activation
// lock, so no activation attempt runs across it: one already running is the latest activation the
// stop records, and none after it is admitted (PrepareActivation). A repeat of the console request
// answers the stop it recorded; another request under its key is ErrRequestReused, and another stop
// while one holds the agent ErrAgentStopped.
func (s *service) Stop(ctx context.Context, in StopInput) (Stop, bool, error) {
	if err := s.inOrganization(ctx, in.Agent, in.Key.Organization); err != nil {
		return Stop{}, false, err
	}
	var (
		stored Stop
		first  bool
	)
	err := s.repository.WithAgentLock(ctx, in.Agent, func(ctx context.Context) error {
		latest, err := s.repository.LatestActivation(ctx, in.Agent)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		stored, first, err = s.repository.RecordStop(ctx, Stop{
			Agent: in.Agent, Key: in.Key, Binding: in.Binding, ActivationID: latest,
		})
		return err
	})
	if err != nil {
		return Stop{}, false, err
	}
	if stored.Agent != in.Agent || stored.Binding != in.Binding {
		return Stop{}, false, ErrRequestReused
	}
	return stored, first, nil
}

// RecordDeactivation records that garam answered the deactivation of the stop stored under key.
func (s *service) RecordDeactivation(ctx context.Context, key RequestKey) error {
	return s.repository.RecordDeactivation(ctx, key)
}

// Start ends the stop holding an agent of the request's organization; the stop is kept. A repeat of
// the console request answers the stop it ended; another request under its key is
// ErrRequestReused, and a start of an agent no stop holds ErrAgentNotStopped.
func (s *service) Start(ctx context.Context, in StopInput) (Stop, error) {
	if err := s.inOrganization(ctx, in.Agent, in.Key.Organization); err != nil {
		return Stop{}, err
	}
	stored, err := s.repository.RecordStart(ctx, in.Agent, StopEnd{Key: in.Key, Binding: in.Binding})
	if err != nil {
		return Stop{}, err
	}
	if stored.Agent != in.Agent || stored.Start == nil || stored.Start.Binding != in.Binding {
		return Stop{}, ErrRequestReused
	}
	return stored, nil
}

// inOrganization refuses an agent with no revision in org as ErrNotFound.
func (s *service) inOrganization(ctx context.Context, agent GRN, org string) error {
	d, err := s.repository.GetDefinition(ctx, agent)
	if err != nil {
		return err
	}
	if d.Organization != org {
		return ErrNotFound
	}
	return nil
}

// stopped refuses an agent a stop holds as ErrAgentStopped.
func (s *service) stopped(ctx context.Context, agent GRN) error {
	_, err := s.repository.CurrentStop(ctx, agent)
	switch {
	case err == nil:
		return ErrAgentStopped
	case errors.Is(err, ErrNotFound):
		return nil
	default:
		return err
	}
}
