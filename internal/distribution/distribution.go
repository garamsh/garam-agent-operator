package distribution

import "errors"

// Proof is garam's positive proof of a controller's current authority, and of one agent's
// placement on it when an agent was named.
type Proof struct {
	Operator string
	Org      string
	Agent    *ProvenAgent
}

// ProvenAgent is the agent's current assignment to the controller.
type ProvenAgent struct {
	GRN   string
	Epoch string
}

var (
	// ErrNoCertificate is returned for a request that presented no client certificate, or one
	// naming no single operator GRN.
	ErrNoCertificate = errors.New("no controller certificate presented")

	// ErrNotProved is returned when garam does not prove what was asked: the certificate does not
	// prove the controller, the controller or agent is unknown, or current authority refuses.
	ErrNotProved = errors.New("controller authority not proved")

	// ErrUndecided is returned when garam could not decide the proof from current state.
	ErrUndecided = errors.New("controller authority undecided")

	// ErrAnotherOperator is returned for a proof naming another operator than the certificate.
	ErrAnotherOperator = errors.New("proof names another operator than the certificate")
)
