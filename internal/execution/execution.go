package execution

import (
	"errors"
	"fmt"
)

// Contract is the agent-execution.v2 value every request and answer of these routes carries in
// Garam-Contract-Version (garam@afde8b8, ADR-0084, ADR-0100).
const Contract = "agent-execution.v2"

// AgentToken is an agent token as the adapter presented it, once it is one in garam's form: never
// stored or logged here.
type AgentToken string

// Introspection is garam's answer to introspectAgentExecution: whether the agent's token and a
// generation are current together, with the agent's assignment and current activation.
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

// ActivationCall is the activateAgent request control sends garam. An empty
// ReplacesActivationID or OperationRef is sent as null. Token is the agent's token minted for this
// service, forwarded as the adapter presented it, and never stored or logged here.
type ActivationCall struct {
	RequestID            string
	Epoch                string
	Generation           string
	ReplacesActivationID string
	OperationRef         string
	Token                AgentToken
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

	// ErrNotAuthorized is returned when garam refuses control's own authority over the agent.
	ErrNotAuthorized = errors.New("garam refused the authority")

	// ErrCredentialRefused is returned when garam refuses the forwarded token as not this agent's
	// token minted for this service: a token minted for garam, another agent's, or one whose row
	// garam no longer holds (422).
	ErrCredentialRefused = errors.New("garam refused the agent's token")

	// ErrActivationSuperseded is returned when garam refuses the activation as another than the
	// one the agent's anchor names, or as one that has ended.
	ErrActivationSuperseded = errors.New("garam refused the activation as superseded")
)

// GaramContractError is returned when garam answered under a contract version this service does
// not take, or under none. Nothing in the answer is read, so it decides nothing.
type GaramContractError struct {
	// Call names garam's route, Contract the contract it answered under, empty for none.
	Call, Contract string
}

func (e *GaramContractError) Error() string {
	return fmt.Sprintf("garam answered %s under contract %q, which this service does not take", e.Call, e.Contract)
}
