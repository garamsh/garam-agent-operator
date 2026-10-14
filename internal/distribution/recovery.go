package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const (
	// kindRecoveryStage names the refusal of a recovery request for a recovery already finalized
	// under another.
	kindRecoveryStage = "recovery_stage"
	// maxRecoveryRequestBytes bounds a recovery request's body.
	maxRecoveryRequestBytes = 4 << 10
)

// requestIDPattern is the request identifier garam takes.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// errInvalidRecoveryBody is returned for a body that is not one recovery request.
var errInvalidRecoveryBody = errors.New("request body is not a recovery request")

// recoveryRequest is the body of a controller's request for its agent's open recovery: the
// recovery's request id on garam and the epoch it expects. It carries no certificate request:
// garam's recovery under execution-fence.v2 signs nothing (garam@afde8b8).
type recoveryRequest struct {
	RequestID string `json:"requestId"`
	Epoch     string `json:"epoch"`
}

// recoveryResponse is an open recovery as its controller sees it: prepared, or finalized with the
// agent's new lineage, in which the controller mints the agent's tokens.
type recoveryResponse struct {
	Agent             string `json:"agent"`
	RecoveryRequestID string `json:"recoveryRequestId"`
	Epoch             string `json:"epoch"`
	Stage             string `json:"stage"`
	Lineage           string `json:"lineage,omitempty"`
}

// prepareRecovery stores the request a controller made for the agent's open recovery as the
// request garam is to be sent (ADR 0057). The controller is proved per request and the agent's
// placement on it per decision. A repeat answers 202 while the recovery is prepared, and 200 with
// the new lineage once garam answered its finalize.
func (s *server) prepareRecovery(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRecoveryRequestBytes))
	if err != nil {
		s.respondError(w, errInvalidRecoveryBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseRecoveryRequest(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.provePlacedAt(r, c, agent, in.Epoch); err != nil {
		s.respondError(w, err)
		return
	}
	stored, err := s.definitions.PrepareRecovery(r.Context(), definition.GRN(agent), in)
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
		answer.Lineage = stored.Recovered.Lineage
	}
	writeJSON(w, status, answer)
}

// parseRecoveryRequest reads exactly one recovery request, refusing a field it does not know: a
// certificate request among them.
func parseRecoveryRequest(body []byte) (definition.RecoveryRequest, error) {
	var in recoveryRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() || !requestIDPattern.MatchString(in.RequestID) || in.Epoch == "" {
		return definition.RecoveryRequest{}, errInvalidRecoveryBody
	}
	return definition.RecoveryRequest{RequestID: in.RequestID, Epoch: in.Epoch}, nil
}
