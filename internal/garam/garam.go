// Package garam reads the agent definitions garam holds for this operator and
// claims them.
package garam

import (
	"errors"
	"time"
)

// GRN is a garam resource name. This operator carries one and never reads
// inside it: what its segments mean is garam's, and an identifier it minted is
// an identifier here.
type GRN string

// Definition is one agent an organization asked this operator to run.
type Definition struct {
	// Agent is the identity garam minted for the agent this definition
	// describes. No agent answers under it until the definition is claimed.
	Agent GRN

	// Tools is the tool set this definition declares, as the keys this operator
	// reads out of garam's answer. garam stores the keys and the strings and
	// interprets neither, so what a key means is this operator's contract rather
	// than garam's.
	Tools ToolSet

	// Ignored names the value keys this operator does not read, sorted. It
	// carries the names and never the strings beside them: a key this operator
	// holds no contract for is one it cannot act on, and the name is the whole
	// of what a report about it can say.
	Ignored []string

	// Claim is the agent's assignment where the definition is claimed, and nil
	// where it is not. garam admits no claimant but the operator a definition
	// names, so a claim it reports here was made by this operator.
	Claim *Claim
}

// ToolSet is what a definition declares about the tools its agent accepts.
type ToolSet struct {
	// Pins name the manifest each tool is accepted at, keyed by the tool's own
	// name, and are nil where the definition declares none. A pin is opaque: it
	// is carried to the agent and read nowhere here.
	Pins map[string]string
}

// Claim is the assignment a definition's claim wrote, as garam reports it now.
//
// It is one value rather than a flag beside an epoch, because the two cannot be
// true separately: a definition is claimed exactly when there is an epoch to
// report for it.
type Claim struct {
	// Epoch is the epoch the agent's assignment holds at now, which is not
	// necessarily the one this operator holds the agent at. A definition names
	// its operator once and is never rewritten, so it stays listed and stays
	// claimed after the agent is assigned elsewhere, carrying whoever holds it
	// by then (garam@e1e69fd:api/machine.yaml:322-330).
	Epoch int64
}

// Assignment binds an agent to the operator that runs it, and is what admits
// this operator to that agent's certificate route.
type Assignment struct {
	// Agent is the agent the assignment binds.
	Agent GRN

	// Operator is the operator it is bound to, which is the caller.
	Operator GRN

	// Epoch rises with every assignment and fences the operator it replaced.
	Epoch int64
}

// AgentCredential is what garam issues an agent: the certificate it
// authenticates as, the private key it was issued with, the authority that
// signed it, and the root it verifies garam's listener by.
//
// The last two are different certificates and are not interchangeable. garam is
// verified against ServerRootPEM and against nothing else; IssuerPEM is the
// authority that signed this certificate, which signs no listener
// (garam@5130ca9:api/machine.yaml:467-479).
type AgentCredential struct {
	// CertificatePEM is the certificate, PEM-encoded.
	CertificatePEM []byte

	// KeyPEM is the private key it was issued with, PEM-encoded. garam stores
	// none, so this arrives once and a holder that loses it asks for another
	// certificate.
	KeyPEM []byte

	// IssuerPEM is the authority that signed the certificate, PEM-encoded.
	IssuerPEM []byte

	// ServerRootPEM is the root garam's listener is verified against,
	// PEM-encoded.
	ServerRootPEM []byte

	// NotAfter is when the certificate stops being valid. garam answers it, so
	// nothing here parses a PEM to learn it.
	NotAfter time.Time
}

// The keys an agent's credential is placed under, which are also the names the
// kubelet gives the files in the volume its workload mounts. They are the names
// garam's own mint writes the three public-and-private files under
// (garam@5130ca9:internal/cli/issue_certificate.go:113-115), with the server
// root that arrives beside them named for what it is.
const (
	CertificateKey = "certificate.pem"
	KeyKey         = "key.pem"
	IssuerKey      = "issuer.pem"
	ServerRootKey  = "server-root.pem"
)

// ErrAgentNotHeld is what garam answers an agent this operator does not hold at
// the current epoch. It answers the same for an agent assigned to another
// operator, for one this operator was replaced on, and for one that does not
// exist, so the three cannot be told apart: this reports "not mine right now"
// and the next read of the definitions is the whole of the response.
var ErrAgentNotHeld = errors.New("garam holds this agent for another operator, or holds no such agent")

// ErrClaimConflict is what a claim of an agent garam already holds a claim for
// answers. A definition is claimed once and a second claim is a conflict rather
// than an override, so retrying one changes nothing.
var ErrClaimConflict = errors.New("garam holds a claim on this agent already")

// ErrReportStale is what garam answers a provisioning report carrying an epoch
// the agent's assignment has moved past. It says this operator holds the agent
// at an epoch that is no longer current, which is a different fact from
// [ErrAgentNotHeld]: that one says the agent is assigned elsewhere or does not
// exist, and this one says the assignment moved and came back
// (garam@e1e69fd:api/machine.yaml:213-220).
//
// Neither is retryable. No route answers the epoch this operator holds an agent
// at, and a definition's claim answers the assignment's rather than this
// operator's, so nothing recovers a stale one.
var ErrReportStale = errors.New("garam holds this agent at an epoch later than the one reported")

// ProvisioningState is what this operator tells garam it sees of an agent's pod.
//
// garam's enum carries four values and this operator sends two of them
// (garam@e1e69fd:api/machine.yaml:605-610). The other two are out of reach
// rather than unimplemented:
//
//   - "pending" is garam's word for an agent no operator has reported on yet.
//     It is the value garam already holds before any report, so an operator
//     reaches it by staying silent; sending it would assert that this operator
//     has never reported while being a report.
//   - "failed" needs telling a replica that cannot start from one that has not
//     started yet. What this operator reads is the workload's ready replica
//     count, which covers both and says neither, so a report of "failed" would
//     assert more than was observed.
type ProvisioningState string

const (
	// StateProvisioned is an agent whose workload this operator built and which
	// reports no ready replica. It says the pod was provisioned and nothing
	// about its health, which is the whole of what a replica count carries when
	// it reads zero.
	StateProvisioned ProvisioningState = "provisioned"

	// StateReady is an agent whose workload reports a ready replica. A replica
	// is ready once its containers are running and this workload carries no
	// readiness probe, so this says the agent's containers are running and
	// nothing about whether the agent inside them works.
	StateReady ProvisioningState = "ready"
)

// Readiness is what this operator observed of an agent's workload, which is the
// only input the state it reports is decided from.
type Readiness int

const (
	// ReadinessUnobserved is an agent whose workload this operator did not read.
	// It is the zero value because an Observation that says nothing observed
	// nothing.
	ReadinessUnobserved Readiness = iota

	// ReadinessNoReplica is a workload reporting no ready replica, which covers
	// a replica still starting as much as one that cannot start.
	ReadinessNoReplica

	// ReadinessReplicaReady is a workload reporting a ready replica.
	ReadinessReplicaReady
)

// Observation is what this operator holds about one agent it constructed: the
// identity garam minted, the epoch it was constructed at, and what its workload
// was last seen to report.
type Observation struct {
	// Agent is the agent this operator constructed for.
	Agent GRN

	// Epoch is the assignment epoch garam held the agent at when this operator
	// constructed it. A report carries it and garam accepts the report only at
	// the epoch the assignment is currently on.
	Epoch int64

	// Readiness is what the workload was last observed to report.
	Readiness Readiness
}
