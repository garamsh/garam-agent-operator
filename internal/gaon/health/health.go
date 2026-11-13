// Package health serves the operator's liveness and readiness probes.
package health

import (
	"net/http"
	"sync/atomic"
)

// Probe records whether the operator's last reconcile pass succeeded.
// It starts not ready: nothing is known until a pass has run.
type Probe struct {
	ok atomic.Bool
}

// Observe records the outcome of one pass.
func (p *Probe) Observe(err error) { p.ok.Store(err == nil) }

// Handler serves /healthz, which answers 200 while the process runs,
// and /readyz, which answers 200 while the last pass succeeded.
func (p *Probe) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if p.ok.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	return mux
}
