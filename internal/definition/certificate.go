package definition

import (
	"context"
	"errors"
	"fmt"
)

// RequestInitialCertificate asks garam for agent's first certificate under the reference of the
// agent's own creation, storing the request before garam is asked. A request equal to the one
// stored is answered from its stored result, or sent again unchanged while that is unknown; any
// other is refused with ErrRequestReused. garam records nothing for a refusal, so neither is the
// request kept: a corrected one may follow.
func (s *service) RequestInitialCertificate(
	ctx context.Context, in InitialCertificateInput,
) (InitialCertificate, bool, error) {
	creation, err := s.repository.CreationOf(ctx, in.Agent)
	if errors.Is(err, ErrNotFound) {
		archived, archiveErr := s.repository.ArchivedRegistration(ctx, in.Agent)
		if archiveErr != nil {
			return InitialCertificate{}, false, archiveErr
		}
		if archived {
			return InitialCertificate{}, false, ErrCreationArchived
		}
	}
	if err != nil {
		return InitialCertificate{}, false, err
	}
	stored, err := s.repository.BeginInitialCertificate(ctx, in.Agent, in.Request)
	if err != nil {
		return InitialCertificate{}, false, err
	}
	if stored.Request != in.Request {
		return InitialCertificate{}, false, ErrRequestReused
	}
	if stored.Issued != nil {
		return stored, false, nil
	}
	issued, err := s.issuer.Issue(ctx, Issuance{
		Agent: in.Agent, Request: in.Request, OperationRef: creation.Binding.OperationRef,
	})
	var refused *IssuanceRefusedError
	switch {
	case errors.As(err, &refused):
		if clearErr := s.repository.ClearInitialCertificate(ctx, in.Agent, in.Request); clearErr != nil {
			return InitialCertificate{}, false, clearErr
		}
		return InitialCertificate{}, false, err
	case err != nil:
		return InitialCertificate{}, false, fmt.Errorf("issue initial certificate: %w", err)
	}
	c, first, err := s.repository.IssueInitialCertificate(ctx, in.Agent, in.Request, issued)
	if err != nil {
		return InitialCertificate{}, false, err
	}
	if c.Request != in.Request {
		return InitialCertificate{}, false, ErrRequestReused
	}
	return c, first, nil
}
