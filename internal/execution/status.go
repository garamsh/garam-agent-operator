package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// statusReport is the body of a runtime-status report: the activation, and what the runtime's
// own status said, unchanged.
type statusReport struct {
	ActivationID   string    `json:"activationId"`
	GRN            string    `json:"grn"`
	Epoch          string    `json:"epoch"`
	Generation     string    `json:"generation"`
	ConfigRevision string    `json:"configRevision"`
	State          string    `json:"state"`
	StartedAt      time.Time `json:"startedAt"`
	ObservedAt     time.Time `json:"observedAt"`
}

// stateServing is the runtime state in which the revision it reports is the one it runs.
const stateServing = "serving"

// reportStatus accepts a runtime's status only from the agent's current credential, for the
// generation garam holds active, under the activation that generation was activated as, on the
// agent's current placement. A report never activates or supersedes anything.
func (s *server) reportStatus(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		s.respondError(w, errInvalidRequest)
		return
	}
	leafPEM, err := agentLeaf(r, agent)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseStatus(body, agent)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.admitReport(r, agent, leafPEM, in); err != nil {
		s.respondError(w, err)
		return
	}
	err = s.definitions.RecordRuntimeStatus(r.Context(), definition.GRN(agent), definition.RuntimeReport{
		ActivationID: in.ActivationID, Generation: in.Generation, ConfigRevision: in.ConfigRevision,
		Serving: in.State == stateServing, ObservedAt: in.ObservedAt,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// admitReport has garam answer, for this report, whether the leaf and the generation are current
// together, and requires the agent's assignment, epoch and activation to be the ones reported.
func (s *server) admitReport(r *http.Request, agent string, leafPEM []byte, in statusReport) error {
	execution, err := s.garam.Introspect(r.Context(), agent, leafPEM, in.Generation)
	if err != nil {
		// agent-execution.v1 lists no not_authorized here; garam's refusal of control's authority
		// stops the adapter's protected work as a fence does.
		return refusalOf(err, errCredentialFenced)
	}
	if execution.Credential != credentialCurrent {
		return errCredentialFenced
	}
	if execution.Generation != generationCurrent || execution.GRN != agent || execution.Epoch != in.Epoch ||
		execution.ActivationID != in.ActivationID {
		return errGenerationNotCurrent
	}
	placement, err := s.definitions.CurrentPlacement(r.Context(), definition.GRN(agent))
	if errors.Is(err, definition.ErrNotFound) {
		return errGenerationNotCurrent
	}
	if err != nil {
		return err
	}
	if execution.Assignee != placement.Controller {
		return errGenerationNotCurrent
	}
	activated, err := s.definitions.ActivationOfGeneration(r.Context(), definition.GRN(agent), in.Generation)
	if errors.Is(err, definition.ErrNotFound) || err == nil && activated != in.ActivationID {
		return errGenerationNotCurrent
	}
	return err
}

// parseStatus reads exactly one report for agent, refusing a field it does not know. An empty
// configRevision is accepted: it is unknown, and changes nothing.
func parseStatus(body []byte, agent string) (statusReport, error) {
	var in statusReport
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() ||
		in.ActivationID == "" || in.GRN != agent || in.Epoch == "" || !generationPattern.MatchString(in.Generation) ||
		(in.State != stateServing && in.State != "draining") || in.StartedAt.IsZero() || in.ObservedAt.IsZero() {
		return statusReport{}, errInvalidRequest
	}
	return in, nil
}
