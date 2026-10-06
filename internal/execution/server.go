package execution

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// Config is what the agent routes are served from.
type Config struct {
	// Definitions holds the placements, the activation requests and the runtime's status.
	Definitions definition.Service
	// Garam answers the execution introspection, the controller proof and the activation.
	Garam Garam
	// Logger receives one line per request that failed for a reason the caller cannot act on.
	Logger *slog.Logger
}

type server struct {
	definitions definition.Service
	garam       Garam
	logger      *slog.Logger
	// foreignContracts holds each contract value garam answered under that these routes do not
	// take, so each is logged once.
	foreignContracts sync.Map
}

// NewHandler returns the agent routes of agent-execution.v1. Each needs the agent's leaf
// certificate in its TLS handshake, naming the agent in the path:
//
//	POST /v1/agents/{agent}/activations     the activation of the agent's runtime generation
//	POST /v1/agents/{agent}/runtime-status  a report of the runtime's status
func NewHandler(c Config) http.Handler {
	s := &server{definitions: c.Definitions, garam: c.Garam, logger: c.Logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agents/{agent}/activations", s.activate)
	mux.HandleFunc("POST /v1/agents/{agent}/runtime-status", s.reportStatus)
	return s.recoverPanics(underContract(mux))
}

// underContract answers every request, refusals included, under the contract's header, and
// refuses one sent under another value before any route reads it.
func underContract(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(contractHeader, Contract)
		if r.Header.Get(contractHeader) != Contract {
			writeRefusal(w, http.StatusBadRequest, kindInvalidRequest, "Garam-Contract-Version must be "+Contract)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPanics answers 500 to a request whose handler panicked, rather than closing its connection.
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("agent handler panicked", "trace_id", newTraceID(), "panic", v)
				w.Header().Set(contractHeader, Contract)
				writeRefusal(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
