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
	// ErrNoToken is returned for a request that presented no bearer token of garam's operator
	// form, or named no controller GRN beside it.
	ErrNoToken = errors.New("no controller token presented")

	// ErrTokenRefused is returned when garam refuses the token the controller presented: it is not
	// live, names another controller or names another audience (introspectController's 422). The
	// controller is answered 401, so it exchanges a fresh token and asks once more.
	ErrTokenRefused = errors.New("garam refused the controller's token")

	// ErrNotProved is returned when garam does not prove what was asked: the controller or agent
	// is unknown, or current authority refuses.
	ErrNotProved = errors.New("controller authority not proved")

	// ErrUndecided is returned when garam could not decide the proof from current state.
	ErrUndecided = errors.New("controller authority undecided")

	// ErrGaramContractUnsupported is returned when garam answered the proof under a contract
	// version this service does not take, or under none. Nothing in the answer is read, so it
	// proves nothing.
	ErrGaramContractUnsupported = errors.New("garam answered under a contract this service does not take")

	// ErrAnotherOperator is returned for a proof naming another operator than the request.
	ErrAnotherOperator = errors.New("proof names another operator than the request")
)
