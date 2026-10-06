package console

import (
	"net/http"
	"strconv"
)

// The CORS answer a registered console origin gets (#220). The console calls with no cookie, so
// Access-Control-Allow-Credentials is never sent.
const (
	corsAllowMethods = "GET, POST, PUT"
	corsAllowHeaders = "Authorization, Content-Type, Garam-Contract-Version"
	// corsMaxAge is how long a browser may cache a preflight answer, ten minutes.
	corsMaxAge = 600
)

// cors answers the console's origins, exactly as registered, and no other. A preflight from a
// registered origin is answered here and reaches no route, since it carries no authority. One
// from any other origin is refused with no CORS header, and an actual request from one is served
// with none, so the browser withholds the answer.
func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		_, registered := s.origins[origin]
		registered = registered && origin != ""
		if len(s.origins) > 0 {
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !registered {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Max-Age", strconv.Itoa(corsMaxAge))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if registered {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		next.ServeHTTP(w, r)
	})
}
