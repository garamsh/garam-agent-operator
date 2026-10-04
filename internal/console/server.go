package console

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// maxBodyBytes bounds a request body; the authority binds the digest of all of it.
const maxBodyBytes = 1 << 20

// Config is what the console's routes are served from.
type Config struct {
	// Definitions is the desired state the routes change.
	Definitions definition.Service
	// Introspector reads each request's operation authority from garam.
	Introspector Introspector
	// Audience is this control service's operator GRN, the audience every authority must name.
	Audience string
	// Now reads the clock an authority's expiry is checked against.
	Now func() time.Time
	// Logger receives one line per request that failed for a reason the caller cannot act on.
	Logger *slog.Logger
}

type server struct {
	definitions  definition.Service
	introspector Introspector
	audience     string
	now          func() time.Time
	logger       *slog.Logger
}

// NewHandler returns the console's routes:
//
//	POST /v1/orgs/{org}/agents/{agent}/revisions  configure an agent's definition
func NewHandler(c Config) http.Handler {
	s := &server{
		definitions:  c.Definitions,
		introspector: c.Introspector,
		audience:     c.Audience,
		now:          c.Now,
		logger:       c.Logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/revisions", s.configure)
	return s.recoverPanics(mux)
}

// recoverPanics answers 500 to a request whose handler panicked, rather than closing its connection.
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.logger.Error("console handler panicked", "trace_id", newTraceID(), "panic", v)
				writeMessage(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
