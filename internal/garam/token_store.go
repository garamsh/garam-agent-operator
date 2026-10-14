package garam

import (
	"context"
	"time"
)

// TokenStore is where the agents this operator holds keep the tokens their
// adapters read: each agent's credential Secret, which only its adapter mounts
// (ADR 0073).
type TokenStore interface {
	// Holders lists every agent this operator built with an identity, on
	// either source, with when its tokens are next due.
	Holders(ctx context.Context) ([]TokenHolder, error)

	// Store writes tokens into the agent's credential Secret, with when they
	// are next due, creating the Secret where it is absent. It refuses to write
	// over a Secret changed since holder was read, so a recovery finalized
	// meanwhile is minted for again rather than overwritten.
	Store(ctx context.Context, holder TokenHolder, tokens Tokens, due time.Time) error
}

// TokenHolder is an agent whose adapter reads the tokens this operator mints.
type TokenHolder struct {
	// Agent is the agent the tokens are minted for.
	Agent GRN

	// Epoch is the assignment epoch the agent's spec names, and zero where it
	// names none. A token garam mints at another epoch is not written.
	Epoch int64

	// Fenced is true for an agent whose adapter activates through the control
	// service, which also reads a token minted for the control service.
	Fenced bool

	// Due is when its tokens are next minted, and zero where none are placed, a
	// finalized recovery asks for new ones, or a fenced agent holds no token for
	// the control service.
	Due time.Time

	// Version is the credential Secret's version when it was read, empty where
	// it is absent. Store writes only over that version.
	Version string
}

// Tokens is what one refresh mints for an agent.
type Tokens struct {
	// Garam is the token garam accepts, minted with no audience.
	Garam AgentToken

	// Control is the token minted for the control service, nil for an unfenced
	// agent.
	Control *AgentToken
}
