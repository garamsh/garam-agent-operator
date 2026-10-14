// Package agentname names the objects this operator builds for an agent GRN.
// Both of the manager's sources of desired state construct Agents, so the rule
// sits below both of them.
package agentname

import (
	"crypto/sha256"
	"encoding/hex"
)

// credentialsSecretSuffix is what an agent's credential Secret is named after
// the Agent it belongs to, and recoveryRequestSecretSuffix the Secret its
// recovery request is persisted in until the recovery is finalized.
const (
	credentialsSecretSuffix     = "-credentials"
	recoveryRequestSecretSuffix = "-recovery-request"
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

// RecoveryRequestSecret is what the Secret persisting a managed agent's recovery
// request is called, until the recovery is finalized (ADR 0057, ADR 0073).
func RecoveryRequestSecret(grn string) string {
	return Agent(grn) + recoveryRequestSecretSuffix
}

// The names the recoverer and the token refresher write onto an agent's
// credential Secret, and the workload's controller and the refresher read off
// it (ADR 0073). They are the manager's, in different domains, so the names sit
// below them.
const (
	// CredentialLineageAnnotation records on an agent's credential Secret the
	// lineage garam recovered it under. It is absent where no recovery was
	// finalized, and the workload's Pod template carries it, so a recovery moves
	// the Pod and an agent never recovered moves nothing.
	CredentialLineageAnnotation = "agent.garam.sh/credential-lineage"

	// TokensRefreshAnnotation records on an agent's credential Secret when its
	// tokens are next minted, in RFC 3339. A Secret without it is minted for on
	// the refresher's next pass: the recoverer removes it once a recovery is
	// finalized, so the agent's tokens are minted again in the new lineage.
	TokensRefreshAnnotation = "agent.garam.sh/tokens-refresh-at"

	// TokensFencedAnnotation records on an agent's credential Secret that its
	// tokens include the one minted for the control service. A fenced agent's
	// Secret without it, as a cutover to the control service leaves one, is
	// minted for on the refresher's next pass.
	TokensFencedAnnotation = "agent.garam.sh/tokens-fenced"
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
