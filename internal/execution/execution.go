package execution

import "errors"

// Contract is the agent-execution.v1 value every request and answer of these routes carries in
// Garam-Contract-Version (garam@e81a1e0, ADR-0084).
const Contract = "agent-execution.v1"

// Introspection is garam's answer to introspectAgentExecution: whether the agent's certificate and
// a generation are current together, with the agent's assignment and current activation.
type Introspection struct {
	GRN          string
	Org          string
	Assignee     string
	Epoch        string
	Mode         string
	Credential   string
	Generation   string
	ActivationID string
}

// ControllerProof is garam's agent-bound proof of a controller: the operator it proves, and the
// agent's assignment to it.
type ControllerProof struct {
	Operator string
	Agent    string
	Epoch    string
}

// ActivationCall is the activateAgent request control sends garam. An empty
// ReplacesActivationID or OperationRef is sent as null.
type ActivationCall struct {
	RequestID            string
	Epoch                string
	Generation           string
	ReplacesActivationID string
	OperationRef         string
	CertificatePEM       []byte
}

// Activation is garam's answer to activateAgent. Token is shown once and is never stored or
// logged here.
type Activation struct {
	ActivationID string
	Token        string
	TokenVersion int
	GRN          string
	Epoch        string
	Generation   string
}

var (
	// ErrUndecided is returned when garam could not decide from current state.
	ErrUndecided = errors.New("garam left the decision undecided")

	// ErrNotAuthorized is returned when garam refuses control's own authority over the agent, or
	// does not prove the controller.
	ErrNotAuthorized = errors.New("garam refused the authority")

	// ErrCredentialRefused is returned when garam refuses the agent's leaf as not the agent's.
	ErrCredentialRefused = errors.New("garam refused the agent's certificate")

	// ErrActivationSuperseded is returned when garam refuses the activation as another than the
	// one the agent's anchor names, or as one that has ended.
	ErrActivationSuperseded = errors.New("garam refused the activation as superseded")
)
