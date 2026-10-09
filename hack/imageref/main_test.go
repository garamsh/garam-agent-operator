package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	registry = "486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/"
	// workflows is the prefix GITHUB_WORKFLOW_REF carries for every workflow of this repository.
	workflows = "garamsh/garam-agent-operator/.github/workflows/"
	// releaseRun is GITHUB_WORKFLOW_REF in release.yml's run for the tag v1.2.3.
	releaseRun = workflows + "release.yml@refs/tags/v1.2.3"
)

func TestCheck_RefusesAHashTagUnderGaram(t *testing.T) {
	_, err := check(registry+"garam/garam-agent-operator:abc123def456", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "garam-dev/garam-agent-operator:<12-hex commit hash>")
	assert.Contains(t, err.Error(), "ADR 0066")

	// Control: the same image and tag under garam-dev/ is accepted.
	_, err = check(registry+"garam-dev/garam-agent-operator:abc123def456", "")
	assert.NoError(t, err)
}

func TestCheck_RefusesADevTagUnderGaramDev(t *testing.T) {
	_, err := check(registry+"garam-dev/garam-agent-operator-control:dev-abc123def456", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "garam-dev/garam-agent-operator-control:abc123def456 instead")
	assert.Contains(t, err.Error(), "ADR 0067")

	// Control: the hash tag alone, in the same repository, is accepted.
	_, err = check(registry+"garam-dev/garam-agent-operator-control:abc123def456", "")
	assert.NoError(t, err)
}

func TestCheck_AcceptsAReleaseOnlyFromTheReleaseWorkflowsRunForItsTag(t *testing.T) {
	_, err := check(registry+"garam/garam-agent-operator:1.2.3", releaseRun)
	assert.NoError(t, err)

	// Controls: each refuses because one thing the release rule asks for is missing.
	refused := map[string]struct{ ref, workflowRef string }{
		"outside GitHub Actions": {registry + "garam/garam-agent-operator:1.2.3", ""},
		"another workflow": {registry + "garam/garam-agent-operator:1.2.3",
			workflows + "checks.yml@refs/tags/v1.2.3"},
		"another version's run": {registry + "garam/garam-agent-operator:1.2.4", releaseRun},
		"a hash tag in the release": {registry + "garam/garam-agent-operator:abc123def456",
			workflows + "release.yml@refs/tags/vabc123def456"},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := check(c.ref, c.workflowRef)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ADR 0066")
		})
	}
}

func TestCheck_RefusesAGaramDevReferenceWithoutATag(t *testing.T) {
	_, err := check(registry+"garam-dev/garam-agent-operator", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADR 0067")
}

func TestCheck_AppliesNoRuleOutsideBothNamespaces(t *testing.T) {
	// The scaffold's defaults, and a repository whose name only starts with "garam".
	for _, ref := range []string{"controller:latest", "control:latest", registry + "garamx/thing:dev-1"} {
		_, err := check(ref, "")
		assert.NoError(t, err, ref)
	}
}

func TestRun_ReportsTheVerdictAndExitsByIt(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, 1, run([]string{registry + "garam/garam-agent-operator:abc123def456"}, "", &out, &errOut))
	assert.Contains(t, errOut.String(), "garam-dev/")
	assert.Empty(t, out.String())

	out.Reset()
	errOut.Reset()
	assert.Equal(t, 0, run([]string{registry + "garam-dev/garam-agent-operator:abc123def456"}, "", &out, &errOut))
	assert.Contains(t, out.String(), "a development image under garam-dev/")
	assert.Empty(t, errOut.String())

	assert.Equal(t, 1, run(nil, "", &out, &errOut))
}
