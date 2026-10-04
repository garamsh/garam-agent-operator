package distribution_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_IgnoresARegression(t *testing.T) {
	e := newEnv(t)
	e.configure(t, agentA, controller, 2)

	status, first := e.report(t, e.withCert, agentA, `{"observedRevision":3,"renderedRevision":2}`)
	require.Equal(t, 200, status, first)

	status, late := e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":1}`)
	require.Equal(t, 200, status, late)
	assert.Equal(t, float64(3), late["observedRevision"])
	assert.Equal(t, float64(2), late["renderedRevision"])

	// Control: a higher revision raises what is stored.
	_, raised := e.report(t, e.withCert, agentA, `{"observedRevision":3,"renderedRevision":3}`)
	assert.Equal(t, float64(3), raised["renderedRevision"])
}

func TestStatus_LeavesAppliedNull(t *testing.T) {
	e := newEnv(t)
	status, out := e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":2}`)
	require.Equal(t, 200, status, out)
	applied, present := out["appliedRevision"]
	assert.True(t, present, "the answer carries no appliedRevision field")
	assert.Nil(t, applied)

	// A report naming applied is not a report this route takes.
	refused, _ := e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":2,"appliedRevision":2}`)
	assert.Equal(t, 400, refused)
}

func TestStatus_RefusesAnAgentNotPlacedHere(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		setup func(e *env)
	}{
		{"latest revision recorded for another controller", agentC, func(*env) {}},
		{"garam proves another epoch", agentB, func(e *env) { e.prover.epochs[agentB] = "8" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.prover.epochs[agentC] = epoch
			tt.setup(e)
			status, out := e.report(t, e.withCert, tt.agent, `{"observedRevision":2,"renderedRevision":2}`)
			assert.Equal(t, 403, status, out)

			// Control: the same report for agentA, placed here under its epoch, is accepted.
			status, out = e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":2}`)
			assert.Equal(t, 200, status, out)
		})
	}
}

func TestStatus_RefusedWithoutAClientCertificate(t *testing.T) {
	e := newEnv(t)
	status, _ := e.report(t, e.withoutCert, agentA, `{"observedRevision":2,"renderedRevision":2}`)
	assert.Equal(t, 401, status)

	// Control: the same report presenting the controller's certificate is accepted.
	status, _ = e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":2}`)
	assert.Equal(t, 200, status)
}

func TestStatus_RefusesARevisionTheAgentDoesNotHave(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{"observedRevision":3,"renderedRevision":2}`, `{"observedRevision":0,"renderedRevision":0}`} {
		status, out := e.report(t, e.withCert, agentA, body)
		assert.Equal(t, 400, status, out)
	}
	// Control: the agent's latest revision is a revision it has.
	status, out := e.report(t, e.withCert, agentA, `{"observedRevision":2,"renderedRevision":2}`)
	assert.Equal(t, 200, status, out)
}
