package definition

import (
	"context"
	"errors"
	"time"
)

// WithActivationLock runs fn while the agent's activation attempts are serialized: one attempt
// at a time per agent, across every instance of the service.
func (s *service) WithActivationLock(ctx context.Context, agent GRN, fn func(context.Context) error) error {
	return s.repository.WithAgentLock(ctx, agent, fn)
}

// PrepareActivation returns the activation request stored under req's key, storing it first
// where none is. A stored one with another request is ErrRequestReused. A new one is sent under
// the agent's latest activation as its anchor, and under the operation reference of the revision
// it runs: none where the runtime reported that revision effective under that anchor, and
// otherwise the reference that produced it, the creation's for revision 1 and the configure
// request's after.
func (s *service) PrepareActivation(ctx context.Context, agent GRN, req ActivationRequest) (Activation, error) {
	stored, err := s.repository.GetActivation(ctx, agent, req.RequestID)
	if err == nil {
		if stored.Request != req {
			return Activation{}, ErrRequestReused
		}
		return stored, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Activation{}, err
	}
	latest, err := s.repository.GetDefinition(ctx, agent)
	if err != nil {
		return Activation{}, err
	}
	if req.ConfigRevision < 1 || req.ConfigRevision > latest.Revision {
		return Activation{}, ErrInvalidStatus
	}
	anchor, err := s.repository.LatestActivation(ctx, agent)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Activation{}, err
	}
	ref, err := s.operationReference(ctx, agent, req.ConfigRevision, anchor)
	if err != nil {
		return Activation{}, err
	}
	return s.repository.InsertActivation(ctx, Activation{
		Agent: agent, Request: req, ReplacesActivationID: anchor, OperationRef: ref,
	})
}

// operationReference is the reference an activation of revision under anchor is sent with.
func (s *service) operationReference(ctx context.Context, agent GRN, revision Revision, anchor string) (string, error) {
	applied, err := s.repository.GetRuntimeApplied(ctx, agent)
	if err == nil && anchor != "" && applied.ActivationID == anchor && applied.Revision == revision {
		return "", nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if revision == 1 {
		creation, err := s.repository.CreationOf(ctx, agent)
		if errors.Is(err, ErrNotFound) {
			return s.cutoverReference(ctx, agent)
		}
		if err != nil {
			return "", err
		}
		return creation.Binding.OperationRef, nil
	}
	return s.repository.ConfigureReference(ctx, agent, revision)
}

// RecordActivation records the activation garam answered for the request, and makes it the
// agent's latest, the next activation's anchor.
func (s *service) RecordActivation(ctx context.Context, agent GRN, requestID, activationID string) error {
	return s.repository.RecordActivation(ctx, agent, requestID, activationID)
}

// ActivationOfGeneration returns the activation garam answered for the agent's generation.
func (s *service) ActivationOfGeneration(ctx context.Context, agent GRN, generation string) (string, error) {
	return s.repository.ActivationOfGeneration(ctx, agent, generation)
}

// RuntimeReport is an accepted runtime report: the activation and generation it was made under,
// the revision it names, whether the runtime is serving it, and when the runtime observed it.
type RuntimeReport struct {
	ActivationID   string
	Generation     string
	ConfigRevision string
	Serving        bool
	ObservedAt     time.Time
}

// RecordRuntimeStatus records an accepted runtime report. The revision it names becomes the one
// the runtime applied only where the runtime is serving it and it is a revision the agent has; an
// empty or unknown one is unknown, never evidence, and changes nothing.
func (s *service) RecordRuntimeStatus(ctx context.Context, agent GRN, report RuntimeReport) error {
	revision, err := ParseRevision(report.ConfigRevision)
	if err != nil || !report.Serving {
		return nil
	}
	latest, err := s.repository.GetDefinition(ctx, agent)
	if err != nil {
		return err
	}
	if revision > latest.Revision {
		return nil
	}
	return s.repository.RecordRuntimeApplied(ctx, agent, RuntimeApplied{
		Revision: revision, ActivationID: report.ActivationID, Generation: report.Generation, ObservedAt: report.ObservedAt,
	})
}
