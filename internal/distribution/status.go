package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// maxStatusBytes bounds a status report's body.
const maxStatusBytes = 4 << 10

type statusRequest struct {
	ObservedRevision int64 `json:"observedRevision"`
	RenderedRevision int64 `json:"renderedRevision"`
}

// statusResponse is the agent's status as stored after a report: what every controller report
// raised so far, and the revision the runtime applied, null until the runtime reports.
type statusResponse struct {
	Agent            string `json:"agent"`
	ObservedRevision int64  `json:"observedRevision"`
	RenderedRevision int64  `json:"renderedRevision"`
	AppliedRevision  *int64 `json:"appliedRevision"`
}

var (
	// errInvalidStatusBody is returned for a body that is not one status report.
	errInvalidStatusBody = errors.New("request body is not a status report")

	// errNotPlaced is returned for a report on an agent whose latest revision is not recorded for
	// the calling controller, or whose placement garam does not prove under that revision's epoch.
	errNotPlaced = errors.New("agent is not placed on this controller under its latest revision")
)

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStatusBytes))
	if err != nil {
		s.respondError(w, errInvalidStatusBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseStatus(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	latest, err := s.definitions.GetDefinition(r.Context(), definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return
	}
	if latest.Assignment == nil || latest.Assignment.Operator != c.grn {
		s.respondError(w, errNotPlaced)
		return
	}
	placed, err := s.placed(r.Context(), c, agent, latest.Assignment.Epoch)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if !placed {
		s.respondError(w, errNotPlaced)
		return
	}
	stored, err := s.definitions.RecordStatus(r.Context(), definition.GRN(agent),
		definition.Revision(in.ObservedRevision), definition.Revision(in.RenderedRevision))
	if err != nil {
		s.respondError(w, err)
		return
	}
	out := statusResponse{Agent: agent, ObservedRevision: int64(stored.Observed), RenderedRevision: int64(stored.Rendered)}
	if stored.Applied != nil {
		applied := int64(*stored.Applied)
		out.AppliedRevision = &applied
	}
	writeJSON(w, http.StatusOK, out)
}

// parseStatus reads exactly one status report, refusing a field it does not know.
func parseStatus(body []byte) (statusRequest, error) {
	var in statusRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() {
		return statusRequest{}, errInvalidStatusBody
	}
	return in, nil
}
