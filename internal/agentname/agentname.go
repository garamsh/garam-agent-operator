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
