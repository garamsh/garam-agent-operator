// Package agentname names the objects this operator builds for an agent GRN.
// Both of the manager's sources of desired state construct Agents, so the rule
// sits below both of them.
package agentname

import (
	"crypto/sha256"
	"encoding/hex"
)

// credentialsSecretSuffix is what an agent's credential Secret is named after
// the Agent it belongs to, and credentialRequestSecretSuffix the Secret its first
// certificate request is persisted in until the credential is placed.
const (
	credentialsSecretSuffix       = "-credentials"
	credentialRequestSecretSuffix = "-credential-request"
	recoveryRequestSecretSuffix   = "-recovery-request"
)

// Agent is what the Agent built for a GRN is called. It is the digest of the
// whole GRN rather than a part of it: what a GRN's segments mean is garam's, and
// a name cut out of one moves when garam's format does, orphaning every object
// already built under the old shape.
func Agent(grn string) string {
	digest := sha256.Sum256([]byte(grn))
	return "agent-" + hex.EncodeToString(digest[:8])
}

// CredentialsSecret is what the Secret holding the agent's credential is called.
// The operator names it because nothing else can: the Agent is constructed here
// and its source carries no name for it.
func CredentialsSecret(grn string) string {
	return Agent(grn) + credentialsSecretSuffix
}

// CredentialRequestSecret is what the Secret persisting a managed agent's first
// certificate request is called, until its credential is placed.
func CredentialRequestSecret(grn string) string {
	return Agent(grn) + credentialRequestSecretSuffix
}

// RecoveryRequestSecret is what the Secret persisting a managed agent's recovery
// certificate request is called, until the recovered credential is placed
// (ADR 0059).
func RecoveryRequestSecret(grn string) string {
	return Agent(grn) + recoveryRequestSecretSuffix
}

// The names the recoverer writes onto an agent's Secrets and the workload's
// controller reads off them (ADR 0059). Both are the manager's, in different
// domains, so the names sit below both.
const (
	// CredentialLineageAnnotation records on an agent's credential Secret the
	// lineage garam recovered it under. It is absent on a first credential, and
	// the workload's Pod template carries it, so a recovered credential moves
	// the Pod and one never recovered moves nothing.
	CredentialLineageAnnotation = "agent.garam.sh/credential-lineage"

	// RecoveryRefusedAnnotation records on an agent's recovery request Secret
	// why the recovered certificate was not placed. Its value is a condition
	// reason.
	RecoveryRefusedAnnotation = "agent.garam.sh/recovery-refused"
)

// The names the workload's controller writes onto an agent's objects and the
// placement registrar reads off them. Both are the manager's, in different
// domains, so the names sit below both.
const (
	// PVCUIDAnnotation records on an agent's Pod the UID of the state claim it
	// started on, written once by the writer fence (ADR 0042).
	PVCUIDAnnotation = "agent.garam.sh/pvc-uid"

	// PreviousPodUIDAnnotation and PreviousWriterStoppedAnnotation record on an
	// agent's placement Secret the Pod the current token's placement replaces,
	// and the hex SHA-256 over the RFC 8785 canonical JSON of the writer-stopped
	// evidence its release recorded. They are written in the patch that mints
	// that token, and absent before the first release.
	PreviousPodUIDAnnotation        = "agent.garam.sh/previous-pod-uid"
	PreviousWriterStoppedAnnotation = "agent.garam.sh/previous-writer-stopped-sha256"

	// PlacementTokenKey is the one key of an agent's placement Secret.
	PlacementTokenKey = "token"

	// AdapterContainer is the name of garam's adapter in an agent's Pod: the
	// one container that reads the placement token.
	AdapterContainer = "adapter"

	placementSecretSuffix = "-placement"
)

// PlacementSecret is what the Secret holding the agent's placement token is
// called, for the Agent named agent.
func PlacementSecret(agent string) string {
	return agent + placementSecretSuffix
}
