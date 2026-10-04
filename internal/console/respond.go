package console

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// errorBody is every refusal's answer. Kind names a refusal a client branches on.
type errorBody struct {
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
}

// kindInvalidAPIKeyRef is the kind a malformed model key reference is refused under.
const kindInvalidAPIKeyRef = "invalid_api_key_ref"

// respondError translates err to its status. It is the only place a status is chosen for an
// error, and the only place one is logged.
func (s *server) respondError(w http.ResponseWriter, err error) {
	var mismatch *MismatchError
	switch {
	case errors.Is(err, ErrNoAuthority):
		w.Header().Set("WWW-Authenticate", "Garam-Operation")
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrAuthorityUnknown):
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrAuthorityForbidden), errors.Is(err, ErrDigestMismatch), errors.As(err, &mismatch):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrAuthorityUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errInvalidBody):
		writeMessage(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, definition.ErrInvalidSecretRef):
		writeJSON(w, http.StatusBadRequest, errorBody{Kind: kindInvalidAPIKeyRef, Message: err.Error()})
	case errors.Is(err, definition.ErrNotFound):
		writeMessage(w, http.StatusNotFound, err.Error())
	case errors.Is(err, definition.ErrStaleRevision), errors.Is(err, definition.ErrRequestReused),
		errors.Is(err, definition.ErrRegistrationConflict), errors.Is(err, definition.ErrAssignmentMoved):
		writeMessage(w, http.StatusConflict, err.Error())
	case errors.Is(err, definition.ErrRegistrationRefused):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, definition.ErrRegistrationUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.logger.Error("console request failed", "trace_id", newTraceID(), "error", err)
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
