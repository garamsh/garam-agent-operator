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

const (
	// kindInvalidAPIKeyRef is the kind a malformed model or embedding key reference is refused under.
	kindInvalidAPIKeyRef = "invalid_api_key_ref"
	// kindEmbeddingRequired is the kind a model the manager could not render for want of its
	// embeddings endpoint is refused under (ADR 0052).
	kindEmbeddingRequired = "embedding_required"
	// kindEmbeddingImmutable is the kind a change to, or removal of, an agent's embedding is
	// refused under (ADR 0052).
	kindEmbeddingImmutable = "embedding_immutable"
	// kindGaramContractUnsupported is the kind garam's answer under a contract this service does
	// not take, or under none, is refused under. It tells the case apart from an undecided garam.
	kindGaramContractUnsupported = "garam_contract_unsupported"
)

// respondError translates err to its status. It is the only place a status is chosen for an
// error, and the only place one is logged.
func (s *server) respondError(w http.ResponseWriter, err error) {
	var (
		mismatch *MismatchError
		cutover  *CutoverRefusal
	)
	switch {
	case errors.As(err, &cutover):
		writeJSON(w, cutover.Status, errorBody{Kind: cutover.Kind, Message: cutover.Message})
	case errors.Is(err, ErrCutoverUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, definition.ErrImportOpen):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindImportOpen, Message: err.Error()})
	case errors.Is(err, definition.ErrAlreadyDefined):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindAlreadyDefined, Message: err.Error()})
	case errors.Is(err, definition.ErrCutoverPending), errors.Is(err, definition.ErrCutoverStage):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindCutoverStage, Message: err.Error()})
	case errors.Is(err, definition.ErrReverseMigrationRequired):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindReverseMigrationRequired, Message: err.Error()})
	case errors.Is(err, ErrNoAuthority):
		w.Header().Set("WWW-Authenticate", "Garam-Operation")
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrAuthorityUnknown):
		writeMessage(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrAuthorityForbidden), errors.Is(err, ErrDigestMismatch), errors.As(err, &mismatch):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrAuthorityUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errInvalidBody), errors.Is(err, errInvalidPublish):
		writeMessage(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, definition.ErrInvalidSecretRef):
		writeJSON(w, http.StatusBadRequest, errorBody{Kind: kindInvalidAPIKeyRef, Message: err.Error()})
	case errors.Is(err, definition.ErrEmbeddingRequired):
		writeJSON(w, http.StatusBadRequest, errorBody{Kind: kindEmbeddingRequired, Message: err.Error()})
	case errors.Is(err, definition.ErrEmbeddingImmutable):
		writeJSON(w, http.StatusConflict, errorBody{Kind: kindEmbeddingImmutable, Message: err.Error()})
	case errors.Is(err, definition.ErrNotFound):
		writeMessage(w, http.StatusNotFound, err.Error())
	case errors.Is(err, definition.ErrStaleRevision), errors.Is(err, definition.ErrRequestReused),
		errors.Is(err, definition.ErrRegistrationConflict), errors.Is(err, definition.ErrAssignmentMoved):
		writeMessage(w, http.StatusConflict, err.Error())
	case errors.Is(err, definition.ErrRegistrationRefused):
		writeMessage(w, http.StatusForbidden, err.Error())
	case errors.Is(err, definition.ErrRegistrationUndecided):
		writeMessage(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrGaramContractUnsupported), errors.Is(err, definition.ErrGaramContractUnsupported):
		// garam's own contract answers a dependency it cannot use with 503 (garam@59fe68d
		// api/machine.yaml:26-31).
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Kind: kindGaramContractUnsupported, Message: err.Error()})
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
