package agent

import "github.com/garamsh/gaon/lib/api"

// conditionReady is the condition the Node Agent writes when the
// readiness probe holds.
const conditionReady = "Ready"

// observe maps what is observed on a MicroVM to the state to report, by
// ADR-0151 §5's table. A phase read through EffectivePhase, so a status
// whose epoch differs from the spec's reads as Pending.
//
// Failed without lastExit is not a failure the table names: it reports
// what is certain, that the MicroVM exists.
func observe(m *api.MicroVM) State {
	switch m.EffectivePhase() {
	case api.MicroVMRunning:
		if isReady(m) {
			return StateReady
		}
	case api.MicroVMFailed:
		if m.Status.LastExit != nil {
			return StateFailed
		}
	}
	return StateProvisioned
}

func isReady(m *api.MicroVM) bool {
	for _, c := range m.Status.Conditions {
		if c.Type == conditionReady {
			return c.Status == api.ConditionTrue
		}
	}
	return false
}
