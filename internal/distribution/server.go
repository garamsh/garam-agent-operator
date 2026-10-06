package distribution

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// Config is what the controller routes are served from.
type Config struct {
	// Definitions is the desired state the routes release and the status they record.
	Definitions definition.Service
	// Prover proves each request's controller, and each agent's placement on it, with garam.
	Prover Prover
	// PollInterval is how often a waiting request for the feed reads the position again.
	PollInterval time.Duration
	// MaxAgents bounds the candidate agents one answer of the feed carries, and so the agent
	// proofs one answer costs. A controller with more is refused rather than answered in part.
	MaxAgents int
	// Logger receives one line per request that failed for a reason the caller cannot act on.
	Logger *slog.Logger
}

type server struct {
	definitions  definition.Service
	prover       Prover
	pollInterval time.Duration
	maxAgents    int
	logger       *slog.Logger
}

// NewHandler returns the controller routes. Each needs the client certificate a controller
// presents in its TLS handshake:
//
//	GET  /v1/operators/self/desired                the desired feed, long-polled
//	POST /v1/operators/self/agents/{agent}/status  a report of what the controller observed and rendered
//	POST /v1/operators/self/agents/{agent}/certificate-requests  a request for the agent's first certificate
//	POST /v1/operators/self/agents/{agent}/placements            a registration of the agent's placement
//	POST /v1/operators/self/agents/{agent}/recovery-requests     the certificate request of the agent's open recovery
func NewHandler(c Config) http.Handler {
	s := &server{
		definitions: c.Definitions, prover: c.Prover, pollInterval: c.PollInterval, maxAgents: c.MaxAgents, logger: c.Logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/operators/self/desired", s.desired)
	mux.HandleFunc("POST /v1/operators/self/agents/{agent}/status", s.status)
	mux.HandleFunc("POST /v1/operators/self/agents/{agent}/certificate-requests", s.requestCertificate)
	mux.HandleFunc("POST /v1/operators/self/agents/{agent}/placements", s.registerPlacement)
	mux.HandleFunc("POST /v1/operators/self/agents/{agent}/recovery-requests", s.prepareRecovery)
	return s.recoverPanics(mux)
}

// recoverPanics answers 500 to a request whose handler panicked, rather than closing its connection.
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("controller handler panicked", "trace_id", newTraceID(), "panic", v)
				writeMessage(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
