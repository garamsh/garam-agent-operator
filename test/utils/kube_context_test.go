package utils_test

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/test/utils"
)

// ownedCluster is the kind cluster the run in these cases created.
const ownedCluster = "garam-agent-operator-test-e2e"

// TestCheckContextRefusesEveryContextButTheRunsOwn is what stands between a
// teardown and a cluster nobody meant to tear down. The accepted case is the
// control: it is the only one that changes when the comparison is removed, so a
// refusal arriving for some other reason fails the test rather than passing it.
func TestCheckContextRefusesEveryContextButTheRunsOwn(t *testing.T) {
	g := NewWithT(t)

	g.Expect(utils.CheckContext("kind-"+ownedCluster, ownedCluster)).To(Succeed())

	for _, current := range []string{
		"admin@garam-dev",                    // the production cluster of #111
		"kind-garam-agent-operator-devcheck", // another run's kind cluster
		"",                                   // no context set, which kind leaves behind
	} {
		g.Expect(utils.CheckContext(current, ownedCluster)).
			To(MatchError(ContainSubstring(current)), "accepted context %q", current)
	}
}
