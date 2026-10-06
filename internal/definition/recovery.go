package definition

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecoveryBody is the request garam's credential recovery is sent for r, as the bytes it is sent
// as: an administrator's agent:recover handoff is minted over exactly these, and garam compares
// its digest with the body it receives (garam@59fe68d api/machine.yaml recoverAgentCredential).
func RecoveryBody(r CertificateRequest) ([]byte, error) {
	body, err := json.Marshal(struct {
		RequestID             string `json:"requestId"`
		Epoch                 string `json:"epoch"`
		CertificateRequestPEM string `json:"certificateRequestPem"`
	}{r.RequestID, r.Epoch, r.CSRPEM})
	if err != nil {
		return nil, fmt.Errorf("encode the recovery request: %w", err)
	}
	return body, nil
}

// OpenRecovery opens a recovery of an agent of the request's organization, under the epoch its
// latest revision was recorded for. A repeat of the console request answers the recovery it
// opened; another request under its key is ErrRequestReused, and another recovery while one is
// open ErrRecoveryOpen.
func (s *service) OpenRecovery(ctx context.Context, in OpenRecoveryInput) (Recovery, bool, error) {
	d, err := s.repository.GetDefinition(ctx, in.Agent)
	if err != nil {
		return Recovery{}, false, err
	}
	if d.Organization != in.Key.Organization {
		return Recovery{}, false, ErrNotFound
	}
	if d.Assignment == nil {
		return Recovery{}, false, ErrCutoverPending
	}
	stored, first, err := s.repository.OpenRecovery(ctx, Recovery{
		Agent: in.Agent, RequestID: in.RequestID, Key: in.Key, Binding: in.Binding,
		Epoch: d.Assignment.Epoch, Stage: RecoveryRequested,
	})
	if err != nil {
		return Recovery{}, false, err
	}
	if stored.Agent != in.Agent || stored.RequestID != in.RequestID || stored.Binding != in.Binding {
		return Recovery{}, false, ErrRequestReused
	}
	return stored, first, nil
}

// RecoveryOf returns the agent's open recovery, else its most recent, where it was opened in org.
func (s *service) RecoveryOf(ctx context.Context, org string, agent GRN) (Recovery, error) {
	r, err := s.repository.LatestRecovery(ctx, agent)
	if err != nil {
		return Recovery{}, err
	}
	if r.Key.Organization != org {
		return Recovery{}, ErrNotFound
	}
	return r, nil
}

// PrepareRecovery stores the certificate request the agent's controller made for its open
// recovery, as the bytes garam is to be sent. A repeat answers the recovery as stored, with
// garam's answer once it is finalized.
func (s *service) PrepareRecovery(ctx context.Context, in InitialCertificateInput) (Recovery, error) {
	body, err := RecoveryBody(in.Request)
	if err != nil {
		return Recovery{}, err
	}
	return s.repository.PrepareRecovery(ctx, in.Agent, in.Request.RequestID, in.Request.Epoch, body)
}

// FinalizeRecovery records garam's answer to the agent's prepared recovery.
func (s *service) FinalizeRecovery(ctx context.Context, agent GRN, requestID string, c RecoveredCredential) (Recovery, error) {
	return s.repository.FinalizeRecovery(ctx, agent, requestID, c)
}
