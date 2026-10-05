package execution

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// contractHeader carries the contract's value on every request and answer.
const contractHeader = "Garam-Contract-Version"

// The refusal kinds agent-execution.v1 lists (garam@e81a1e0, ADR-0084).
const (
	kindInvalidRequest       = "invalid_request"
	kindUnauthenticated      = "unauthenticated"
	kindPlacementNotCurrent  = "placement_not_current"
	kindCredentialFenced     = "credential_fenced"
	kindNotAuthorized        = "not_authorized"
	kindRequestReused        = "request_reused"
	kindEpochSuperseded      = "epoch_superseded"
	kindActivationSuperseded = "activation_superseded"
	kindGenerationNotCurrent = "generation_not_current"
	kindUndecided            = "undecided"
)

// refusal is an answer agent-execution.v1 names: a status and a kind.
type refusal struct {
	status  int
	kind    string
	message string
}

func (r *refusal) Error() string { return r.message }

var (
	errInvalidRequest       = &refusal{http.StatusBadRequest, kindInvalidRequest, "request is not one this route takes"}
	errUnauthenticated      = &refusal{http.StatusUnauthorized, kindUnauthenticated, "no agent certificate naming one GRN, or no placement token"}
	errAnotherAgent         = &refusal{http.StatusForbidden, kindNotAuthorized, "the certificate names another agent than the path"}
	errNotAuthorized        = &refusal{http.StatusForbidden, kindNotAuthorized, "garam refused the authority this activation needs"}
	errPlacementNotCurrent  = &refusal{http.StatusForbidden, kindPlacementNotCurrent, "the placement is not the agent's current one"}
	errCredentialFenced     = &refusal{http.StatusForbidden, kindCredentialFenced, "the agent's certificate is not its current credential"}
	errRequestReused        = &refusal{http.StatusConflict, kindRequestReused, "request id reused for another activation"}
	errEpochSuperseded      = &refusal{http.StatusConflict, kindEpochSuperseded, "the epoch is not the agent's current assignment"}
	errActivationSuperseded = &refusal{http.StatusConflict, kindActivationSuperseded, "garam refused the activation as superseded"}
	errGenerationNotCurrent = &refusal{http.StatusConflict, kindGenerationNotCurrent, "the report is not of the agent's active generation"}
	errUndecided            = &refusal{http.StatusServiceUnavailable, kindUndecided, "garam or the store could not decide"}
)

// errorBody is every refusal's answer.
type errorBody struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// respondError translates err to its answer. It is the only place these routes choose a status
// for an error, and the only place one is logged.
func (s *server) respondError(w http.ResponseWriter, err error) {
	var named *refusal
	switch {
	case errors.As(err, &named):
		writeRefusal(w, named.status, named.kind, named.message)
	case errors.Is(err, definition.ErrRequestReused):
		writeRefusal(w, errRequestReused.status, errRequestReused.kind, errRequestReused.message)
	case errors.Is(err, definition.ErrInvalidStatus):
		writeRefusal(w, errInvalidRequest.status, errInvalidRequest.kind, "configRevision is not a revision the agent has")
	case errors.Is(err, definition.ErrNotFound):
		// No reference control could send for the revision: nothing authorizes the activation.
		writeRefusal(w, errNotAuthorized.status, errNotAuthorized.kind, "control holds no authority to activate this revision")
	case errors.Is(err, ErrUndecided):
		writeRefusal(w, errUndecided.status, errUndecided.kind, err.Error())
	default:
		s.logger.Error("agent request failed", "trace_id", newTraceID(), "error", err)
		writeRefusal(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeRefusal(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, errorBody{Kind: kind, Message: message})
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
