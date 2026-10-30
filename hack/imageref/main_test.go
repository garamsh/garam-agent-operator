package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	registry = "486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/"
	// manager and control are the two images' repositories.
	manager = registry + "garam-agent-operator/manager"
	control = registry + "garam-agent-operator/control"
	// workflows is the prefix GITHUB_WORKFLOW_REF carries for every workflow of this repository.
	workflows = "garamsh/garam-agent-operator/.github/workflows/"
	// releaseRun is GITHUB_WORKFLOW_REF in release.yml's run for the tag v1.2.3.
	releaseRun = workflows + "release.yml@refs/tags/v1.2.3"
)

func TestCheck_AcceptsAReleaseTagOnlyInTheReleaseWorkflowsRunForThatVersion(t *testing.T) {
	for _, repository := range []string{manager, control} {
		verdict, err := check(repository+":1.2.3", releaseRun)
		require.NoError(t, err)
		assert.Contains(t, verdict, "a release")
	}

	// Controls: each refuses because one thing the release rule asks for is missing.
	refused := map[string]struct{ ref, workflowRef string }{
		"outside GitHub Actions": {manager + ":1.2.3", ""},
		"another workflow":       {manager + ":1.2.3", workflows + "checks.yml@refs/tags/v1.2.3"},
		"a branch's run":         {manager + ":1.2.3", workflows + "release.yml@refs/heads/main"},
		"another version's run":  {manager + ":1.2.4", releaseRun},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := check(c.ref, c.workflowRef)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "garam-agent-operator/manager:<12-hex commit hash> instead")
			assert.Contains(t, err.Error(), "ADR 0078")
		})
	}
}

func TestCheck_AcceptsADevelopmentImageOnlyUnderItsTwelveHexHashAlone(t *testing.T) {
	for _, repository := range []string{manager, control} {
		verdict, err := check(repository+":abc123def456", "")
		require.NoError(t, err)
		assert.Contains(t, verdict, "a development image")
	}

	// Controls: each is one edit away from the tag accepted above.
	refused := map[string]string{
		"a dev- tag":      control + ":dev-abc123def456",
		"a non-hex digit": control + ":abc123def45g",
		"eleven digits":   control + ":abc123def45",
		"thirteen digits": control + ":abc123def4567",
		"upper case":      control + ":ABC123DEF456",
		"a moving tag":    control + ":latest",
		"a v in front":    control + ":v1.2.3",
		"no tag":          control,
	}
	for name, ref := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := check(ref, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "garam-agent-operator/control:<12-hex commit hash> instead")
			assert.Contains(t, err.Error(), "ADR 0078")
		})
	}
}

func TestCheck_ARefusedDevelopmentTagStaysRefusedInAReleaseRun(t *testing.T) {
	_, err := check(manager+":dev-abc123def456", releaseRun)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADR 0078")

	// Control: the same run accepts its own version.
	_, err = check(manager+":1.2.3", releaseRun)
	assert.NoError(t, err)
}

func TestCheck_AppliesNoRuleOutsideThePublishingRepository(t *testing.T) {
	// Tags the rule refuses: the scaffold's defaults, a repository whose name only starts with the
	// prefix, the prefix as a repository of its own, and another project's repositories.
	for _, ref := range []string{
		"controller:latest",
		"control:latest",
		registry + "garam-agent-operatorx/manager:dev-1",
		registry + "garam-agent-operator:1.2.3",
		registry + "garam/test:1.2.3",
		registry + "garam-dev/garam-test:dev-1",
	} {
		verdict, err := check(ref, "")
		require.NoError(t, err, ref)
		assert.Contains(t, verdict, "no rule applies", ref)
	}

	// Control: the same tags under the prefix are refused, so the path is what exempted them.
	for _, tag := range []string{"latest", "dev-1", "1.2.3"} {
		_, err := check(manager+":"+tag, "")
		assert.Error(t, err, tag)
	}
}

func TestRun_ReportsTheVerdictAndExitsByIt(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, 1, run([]string{manager + ":1.2.3"}, "", &out, &errOut))
	assert.Contains(t, errOut.String(), "ADR 0078")
	assert.Empty(t, out.String())

	out.Reset()
	errOut.Reset()
	assert.Equal(t, 0, run([]string{manager + ":abc123def456"}, "", &out, &errOut))
	assert.Contains(t, out.String(), "a development image")
	assert.Empty(t, errOut.String())

	assert.Equal(t, 1, run(nil, "", &out, &errOut))
}
