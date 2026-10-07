package console

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// The kinds a recovery refusal of this service's own is answered under, beside garam's.
const (
	kindRecoveryOpen     = "recovery_open"
	kindRecoveryStage    = "recovery_stage"
	kindRecoveryMismatch = "recovery_mismatch"
)

// garamRequestID is the request identifier garam takes (garam@59fe68d api/machine.yaml
// CredentialRecoveryRequest).
var garamRequestID = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// openRecoveryRequest opens a recovery: requestId is the authority's, recoveryRequestId the one
// the recovery is sent to garam under. garam binds a request id to one body, so they differ.
type openRecoveryRequest struct {
	RequestID         string `json:"requestId"`
	RecoveryRequestID string `json:"recoveryRequestId"`
}

// finalizeRequest is the body garam's recovery is sent: the prepared bytes, unchanged.
type finalizeRequest struct {
	RequestID             string `json:"requestId"`
	Epoch                 string `json:"epoch"`
	CertificateRequestPEM string `json:"certificateRequestPem"`
}

// recoveryAnswer is a recovery as stored. Body is the exact request garam is to be sent, once
// prepared, which the finalizing handoff is minted over; the credential is garam's answer, once
// finalized.
type recoveryAnswer struct {
	Agent             string  `json:"agent"`
	RecoveryRequestID string  `json:"recoveryRequestId"`
	Epoch             string  `json:"epoch"`
	Stage             string  `json:"stage"`
	Body              *string `json:"body"`
	BodySHA256        *string `json:"bodySha256"`
	Lineage           *string `json:"lineage,omitempty"`
	CertificatePEM    *string `json:"certificatePem,omitempty"`
	IssuerPEM         string  `json:"issuerPem,omitempty"`
	ServerRootPEM     string  `json:"serverRootPem,omitempty"`
}

func recoveryAnswerOf(r definition.Recovery) recoveryAnswer {
	a := recoveryAnswer{
		Agent: string(r.Agent), RecoveryRequestID: r.RequestID, Epoch: r.Epoch, Stage: string(r.Stage),
	}
	if r.Body != nil {
		body := string(r.Body)
		digest := sha256.Sum256(r.Body)
		sum := hex.EncodeToString(digest[:])
		a.Body, a.BodySHA256 = &body, &sum
	}
	if r.Recovered != nil {
		a.Lineage, a.CertificatePEM = &r.Recovered.Lineage, &r.Recovered.CertificatePEM
		a.IssuerPEM, a.ServerRootPEM = r.Recovered.IssuerPEM, r.Recovered.ServerRootPEM
	}
	return a
}

// openRecovery opens an agent's credential recovery under an agent:recover authority bound to this
// body. The authority is checked here and never sent to garam: the handoff garam spends is the
// one finalize carries, minted over the request the agent's controller prepares.
func (s *server) openRecovery(w http.ResponseWriter, r *http.Request) {
	org, agent := r.PathValue("org"), r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return
	}
	b, err := s.authorize(r.Context(), r, body, target{org: org, operation: OperationRecover, grn: agent})
	if err != nil {
		s.respondError(w, err)
		return
	}
	var in openRecoveryRequest
	if err := decodeStrict(body, &in); err != nil || in.RequestID == "" ||
		!garamRequestID.MatchString(in.RecoveryRequestID) {
		s.respondError(w, errInvalidBody)
		return
	}
	if in.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: fieldRequestID})
		return
	}
	opened, first, err := s.definitions.OpenRecovery(r.Context(), definition.OpenRecoveryInput{
		Key:       definition.RequestKey{Organization: org, RequestID: b.RequestID},
		Binding:   bindingOf(b),
		Agent:     definition.GRN(agent),
		RequestID: in.RecoveryRequestID,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	status := http.StatusOK
	if first {
		status = http.StatusCreated
	}
	writeJSON(w, status, recoveryAnswerOf(opened))
}

// getRecovery answers the agent's open recovery, else its most recent, under agent:execution-read
// bound to this route: an administrator reads the prepared request here to mint its handoff.
func (s *server) getRecovery(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if _, _, ok := s.authorized(w, r, OperationExecutionRead, agent); !ok {
		return
	}
	recovery, err := s.definitions.RecoveryOf(r.Context(), r.PathValue("org"), definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryAnswerOf(recovery))
}

// finalizeRecovery sends garam the prepared recovery request under the agent:recover handoff
// minted over exactly its bytes, and records garam's answer. A recovery already finalized is
// answered from the store; one garam left undecided stays prepared, and the same body under the
// same handoff is sent again.
func (s *server) finalizeRecovery(w http.ResponseWriter, r *http.Request) {
	org, agent := r.PathValue("org"), r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return
	}
	b, err := s.authorize(r.Context(), r, body, target{org: org, operation: OperationRecover, grn: agent})
	if err != nil {
		s.respondError(w, err)
		return
	}
	var in finalizeRequest
	if err := decodeStrict(body, &in); err != nil || in.RequestID == "" {
		s.respondError(w, errInvalidBody)
		return
	}
	if in.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: fieldRequestID})
		return
	}
	recovery, err := s.definitions.RecoveryOf(r.Context(), org, definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return
	}
	switch {
	case recovery.RequestID != in.RequestID:
		s.respondError(w, definition.ErrNotFound)
		return
	case recovery.Stage == definition.RecoveryRequested:
		s.respondError(w, definition.ErrRecoveryStage)
		return
	case !bytes.Equal(recovery.Body, body):
		s.respondError(w, definition.ErrRecoveryMismatch)
		return
	case recovery.Stage == definition.RecoveryFinalized:
		writeJSON(w, http.StatusOK, recoveryAnswerOf(recovery))
		return
	}
	// garam resolves the recovery's handoff itself (garam@59fe68d internal/agent/execution_service.go,
	// ResolveHandoff), so it is sent the handoff the administrator presented here, not its reference.
	handoff := Authority(strings.TrimPrefix(r.Header.Get("Authorization"), authorizationScheme))
	recovered, err := s.lifecycle.Recover(r.Context(), agent, handoff, body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	finalized, err := s.definitions.FinalizeRecovery(r.Context(), definition.GRN(agent), in.RequestID, recovered)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, recoveryAnswerOf(finalized))
}

// bindingOf is what an authority bound, as the store keeps it with the request.
func bindingOf(b Binding) definition.Binding {
	stored := definition.Binding{
		Actor: b.Actor, Operation: b.Operation, Target: b.Target, BodySHA256: b.BodySHA256, OperationRef: b.OperationRef,
	}
	if b.Assignment != nil {
		stored.Assignment = definition.Assignment{Operator: b.Assignment.Operator, Epoch: b.Assignment.Epoch}
	}
	return stored
}
