package distribution

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// errorBody is every refusal's answer. Kind is set where a caller has to tell one refusal from
// the others under the same status.
type errorBody struct {
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
}

const (
	// kindTooManyAgents names the refusal of a controller with more candidates than one answer carries.
	kindTooManyAgents = "too_many_agents"
	// kindEpochSuperseded names the refusal of a request whose epoch is not the agent's proved one.
	kindEpochSuperseded = "epoch_superseded"
	// kindRequestReused names the refusal of a certificate request other than the one stored.
	kindRequestReused = "request_reused"
	// kindInvalidRequest names the refusal of a placement registration that is not one.
	kindInvalidRequest = "invalid_request"
	// kindGaramContractUnsupported names garam's answer under a contract this service does not
	// take, or under none, apart from an undecided garam.
	kindGaramContractUnsupported = "garam_contract_unsupported"
)

// placementRefusals is the kind each refusal of a placement registration is answered 409 under.
var placementRefusals = []struct {
	err  error
	kind string
}{
	{definition.ErrPlacementSuperseded, "placement_superseded"},
	{definition.ErrPreviousMismatch, "previous_mismatch"},
	{definition.ErrEvidenceMissing, "evidence_missing"},
	{definition.ErrPlacementConflict, "placement_conflict"},
}

// refusalStatus is the status each class of garam's refusal of an issuance is answered with.
var refusalStatus = map[definition.Refusal]int{
	definition.RefusalForbidden: http.StatusForbidden,
	definition.RefusalConflict:  http.StatusConflict,
	definition.RefusalInvalid:   http.StatusBadRequest,
}

// respondError translates err to its status. It is the only place a controller route chooses a
// status for an error, and the only place one is logged.
func (s *server) respondError(w http.ResponseWriter, err error) {
	for _, refusal := range placementRefusals {
		if errors.Is(err, refusal.err) {
			writeJSON(w, http.StatusConflict, errorBody{Kind: refusal.kind, Message: err.Error()})
			return
		}
	}
	var refused *definition.IssuanceRefusedError
	switch {
	case errors.Is(err, errInvalidPlacementBody):
		writeJSON(w, http.StatusBadRequest, errorBody{Kind: kindInvalidRequest, Message: err.Error()})
	case errors.As(err, &refused) && refusalStatus[refused.Refusal] != 0:
		writeJSON(w, refusalStatus[refused.Refusal], errorBody{Kind: refused.Kind, Message: err.Error()})
	case errors.Is(err, errEpochSuperseded):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindEpochSuperseded, Message: err.Error()})
	case errors.Is(err, definition.ErrRequestReused):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindRequestReused, Message: err.Error()})
	case errors.Is(err, ErrNoCertificate):
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrNotProved), errors.Is(err, ErrAnotherOperator), errors.Is(err, errNotPlaced):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrUndecided), errors.Is(err, definition.ErrIssuanceUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrGaramContractUnsupported), errors.Is(err, definition.ErrGaramContractUnsupported):
		// garam's own contract answers a dependency it cannot use with 503 (garam@59fe68d
		// api/machine.yaml:26-31).
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Kind: kindGaramContractUnsupported, Message: err.Error()})
	case errors.Is(err, errInvalidQuery), errors.Is(err, errInvalidStatusBody), errors.Is(err, definition.ErrInvalidStatus),
		errors.Is(err, errInvalidCertificateBody):
		writeMessage(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, definition.ErrNotFound):
		writeMessage(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errTooManyAgents):
		// A capacity limit that retrying cannot clear, so not a 5xx a caller retries.
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Kind: kindTooManyAgents, Message: err.Error()})
	default:
		s.logger.Error("controller request failed", "trace_id", newTraceID(), "error", err)
		writeMessage(w, http.StatusInternalServerError, "internal error")
	}
}

func writeMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// newTraceID is a locally generated trace id; the control service carries no tracing context.
func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
