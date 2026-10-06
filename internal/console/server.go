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
	// Cutover carries a legacy agent's cutover stages to garam.
	Cutover Cutover
	// Audience is this control service's operator GRN, the audience every authority must name.
	Audience string
	// Now reads the clock an authority's expiry is checked against.
	Now func() time.Time
	// Logger receives one line per request that failed for a reason the caller cannot act on.
	Logger *slog.Logger
	// ConsoleOrigins are the browser origins the console calls from, each exactly as a browser
	// sends it in Origin. Only these get a CORS answer; none means no CORS answer at all.
	ConsoleOrigins []string
}

type server struct {
	definitions  definition.Service
	introspector Introspector
	cutover      Cutover
	audience     string
	now          func() time.Time
	logger       *slog.Logger
	origins      map[string]struct{}
}

// NewHandler returns the console's routes:
//
//	POST /v1/orgs/{org}/agents                    create an agent on a controller
//	POST /v1/orgs/{org}/agents/{agent}/revisions  configure an agent's definition
//	POST /v1/orgs/{org}/agents/{agent}/cutover/{import,freeze,switch,rollback}  one cutover stage
//	GET  /v1/orgs/{org}/templates                     agent-template:read     the latest version of each template
//	GET  /v1/orgs/{org}/templates/{name}/versions/{v} agent-template:read     one template version
//	POST /v1/orgs/{org}/templates/{name}/versions     agent-template:publish  publish a template's next version
//	GET  /v1/orgs/{org}/profiles                      execution-profile:read  every published profile version
//	GET  /v1/orgs/{org}/profiles/{name}/versions/{v}  execution-profile:read  one profile version
//	GET  /v1/orgs/{org}/agents/{agent}/execution      agent:execution-read    an agent's execution
func NewHandler(c Config) http.Handler {
	s := &server{
		definitions:  c.Definitions,
		introspector: c.Introspector,
		cutover:      c.Cutover,
		audience:     c.Audience,
		now:          c.Now,
		logger:       c.Logger,
		origins:      map[string]struct{}{},
	}
	for _, origin := range c.ConsoleOrigins {
		s.origins[origin] = struct{}{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/orgs/{org}/agents", s.create)
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/revisions", s.configure)
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/cutover/import", s.importCutover)
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/cutover/freeze", s.freezeCutover)
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/cutover/switch", s.switchCutover)
	mux.HandleFunc("POST /v1/orgs/{org}/agents/{agent}/cutover/rollback", s.rollBackCutover)
	mux.HandleFunc("GET /v1/orgs/{org}/templates", s.listTemplates)
	mux.HandleFunc("GET /v1/orgs/{org}/templates/{name}/versions/{version}", s.getTemplate)
	mux.HandleFunc("POST /v1/orgs/{org}/templates/{name}/versions", s.publish)
	mux.HandleFunc("GET /v1/orgs/{org}/profiles", s.listProfiles)
	mux.HandleFunc("GET /v1/orgs/{org}/profiles/{name}/versions/{version}", s.getProfile)
	mux.HandleFunc("GET /v1/orgs/{org}/agents/{agent}/execution", s.execution)
	return s.recoverPanics(s.cors(mux))
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
