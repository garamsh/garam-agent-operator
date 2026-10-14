package execution

import (
	"net/http"
	"regexp"
	"strings"
)

const (
	// tokenScheme is the Authorization scheme the adapter presents its control token under
	// (agent-execution.v2, garam@afde8b8 docs/architecture/adapter.md §Token mode).
	tokenScheme = "Garam-Token "
	// placementHeader carries the placement token on an activation (agent-execution.v2).
	placementHeader = "Garam-Placement"
)

// agentTokenPattern is an agent token in garam's form (garam@afde8b8 api/machine.yaml AgentToken).
var agentTokenPattern = regexp.MustCompile(`^garam_at_[0-9a-f]{64}$`)

// agentToken is the token the adapter presented in Authorization, once it is one in garam's form.
// Whether it is the agent's token minted for this service, and current, is garam's to decide per
// request: nothing here reads inside it.
func agentToken(r *http.Request) (AgentToken, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), tokenScheme)
	if !ok || !agentTokenPattern.MatchString(token) {
		return "", errUnauthenticated
	}
	return AgentToken(token), nil
}
