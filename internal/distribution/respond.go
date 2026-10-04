package distribution

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

type errorBody struct {
	Message string `json:"message"`
}

// respondError translates err to its status. It is the only place a controller route chooses a
// status for an error, and the only place one is logged.
func (s *server) respondError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoCertificate):
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrNotProved), errors.Is(err, ErrAnotherOperator), errors.Is(err, errNotPlaced):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errInvalidQuery), errors.Is(err, errInvalidStatusBody), errors.Is(err, definition.ErrInvalidStatus):
		writeMessage(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, definition.ErrNotFound):
		writeMessage(w, http.StatusNotFound, err.Error())
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
