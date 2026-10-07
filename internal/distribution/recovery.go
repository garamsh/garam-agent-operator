package distribution

import (
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// kindRecoveryStage names the refusal of a certificate request for a recovery already finalized
// under another.
const kindRecoveryStage = "recovery_stage"

// recoveryResponse is an open recovery as its controller sees it: prepared, or finalized with the
// credential garam recovered. It carries no private key.
type recoveryResponse struct {
	Agent             string `json:"agent"`
	RecoveryRequestID string `json:"recoveryRequestId"`
	Epoch             string `json:"epoch"`
	Stage             string `json:"stage"`
	Lineage           string `json:"lineage,omitempty"`
	CertificatePEM    string `json:"certificatePem,omitempty"`
	// IssuerPEM and ServerRootPEM are the chain garam answered the recovery with, absent for one a
	// garam before f54b9e8 answered (ADR 0062).
	IssuerPEM     string `json:"issuerPem,omitempty"`
	ServerRootPEM string `json:"serverRootPem,omitempty"`
}

// prepareRecovery stores the certificate request a controller made for the agent's open recovery
// over a key it holds, as the request garam is to be sent (ADR 0057). The controller is proved per
// request and the agent's placement on it per decision, as for a first certificate. A repeat
// answers 202 while the recovery is prepared, and 200 with the recovered credential once garam
// answered its finalize.
func (s *server) prepareRecovery(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCertificateRequestBytes))
	if err != nil {
		s.respondError(w, errInvalidCertificateBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseCertificateRequest(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.provePlacedAt(r, c, agent, in.Epoch); err != nil {
		s.respondError(w, err)
		return
	}
	stored, err := s.definitions.PrepareRecovery(r.Context(), definition.InitialCertificateInput{
		Agent: definition.GRN(agent), Controller: c.grn, Request: in,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	answer := recoveryResponse{
		Agent: agent, RecoveryRequestID: stored.RequestID, Epoch: stored.Epoch, Stage: string(stored.Stage),
	}
	status := http.StatusAccepted
	if stored.Recovered != nil {
		status = http.StatusOK
		answer.Lineage, answer.CertificatePEM = stored.Recovered.Lineage, stored.Recovered.CertificatePEM
		answer.IssuerPEM, answer.ServerRootPEM = stored.Recovered.IssuerPEM, stored.Recovered.ServerRootPEM
	}
	writeJSON(w, status, answer)
}
