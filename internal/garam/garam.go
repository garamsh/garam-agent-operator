// Package garam keeps the garam tokens of the agents this operator holds
// current, minting them at garam's machine listener.
package garam

import (
	"errors"
	"time"
)

// GRN is a garam resource name. This operator carries one and never reads
// inside it: what its segments mean is garam's, and an identifier it minted is
// an identifier here.
type GRN string

// AgentToken is a token garam minted for an agent this operator holds
// (mintAgentToken, garam@afde8b8 api/machine.yaml): the token its adapter
// presents, the epoch it was minted at, when garam stops accepting it, and the
// garam server root the adapter verifies garam's listener by. The token is
// answered once and is never logged.
type AgentToken struct {
	// Token is the token itself, garam_at_ and 64 lowercase hex digits, the
	// whole of the file the adapter reads it from.
	Token string

	// Epoch is the assignment epoch garam minted it at.
	Epoch int64

	// ExpiresAt is when garam stops accepting it.
	ExpiresAt time.Time

	// ServerRootPEM is the root garam's listener is verified against,
	// PEM-encoded.
	ServerRootPEM []byte
}

// The keys an agent's tokens are kept under in its credential Secret, which
// are also the names the kubelet gives the files in the volume its adapter
// mounts (ADR 0073).
const (
	// TokenKey holds the agent token minted with no audience, which garam
	// accepts.
	TokenKey = "token"
	// ControlTokenKey holds the agent token minted with the control service as
	// its audience.
	ControlTokenKey = "control-token"
	// ServerRootKey holds the garam server root the mint answered beside the
	// token.
	ServerRootKey = "server-root.pem"
)

// ErrAgentNotHeld is what garam answers an agent this operator does not hold at
// the current epoch. It answers the same for an agent assigned to another
// operator, for one this operator was replaced on, and for one that does not
// exist, so the three cannot be told apart: this reports "not mine right now".
var ErrAgentNotHeld = errors.New("garam holds this agent for another operator, or holds no such agent")
