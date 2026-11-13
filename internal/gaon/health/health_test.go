package health_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/garam-agent-operator/internal/gaon/health"
)

func status(h http.Handler, method, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

func TestProbeIsNotReadyUntilAPassSucceeded(t *testing.T) {
	p := &health.Probe{}
	h := p.Handler()
	assert.Equal(t, 200, status(h, "GET", "/healthz"))
	assert.Equal(t, 503, status(h, "GET", "/readyz"))

	p.Observe(nil)
	assert.Equal(t, 200, status(h, "GET", "/readyz"))

	p.Observe(errors.New("pass failed"))
	assert.Equal(t, 503, status(h, "GET", "/readyz"))
	assert.Equal(t, 200, status(h, "GET", "/healthz"), "liveness does not follow the pass")

	assert.Equal(t, 405, status(h, "POST", "/readyz"))
}
