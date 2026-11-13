package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	registry = "486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/"
	// control is the repository the release publishes, and other a second repository under the same
	// prefix, which the check holds to the same rules because it names no repository.
	other   = registry + "garam-agent-operator/other"
	control = registry + "garam-agent-operator/control"
	// workflows is the prefix GITHUB_WORKFLOW_REF carries for every workflow of this repository.
	workflows = "garamsh/garam-agent-operator/.github/workflows/"
	// releaseRun is GITHUB_WORKFLOW_REF in release.yml's run for the tag v0.1.0-dev.5.
	releaseRun = workflows + "release.yml@refs/tags/v0.1.0-dev.5"
)

// releaseRunFor is GITHUB_WORKFLOW_REF in release.yml's run for the git tag v<version>.
func releaseRunFor(version string) string {
	return workflows + "release.yml@refs/tags/v" + version
}

func TestCheck_AcceptsEachReleaseFormOnlyInTheReleaseWorkflowsRunForThatVersion(t *testing.T) {
	for _, version := range []string{"0.1.0-dev.5", "0.1.0-dev.12", "0.1.0-rc.1", "0.1.0"} {
		for _, repository := range []string{other, control} {
			verdict, err := check(repository+":"+version, releaseRunFor(version))
			require.NoError(t, err, version)
			assert.Contains(t, verdict, "a release")

			// Control: the same reference outside the release run is refused, so the run is what
			// accepted it.
			_, err = check(repository+":"+version, "")
			assert.Error(t, err, version)
		}
	}

	// Controls: each refuses because one thing the release rule asks for is missing.
	refused := map[string]struct{ ref, workflowRef string }{
		"outside GitHub Actions": {other + ":0.1.0-dev.5", ""},
		"another workflow":       {other + ":0.1.0-dev.5", workflows + "checks.yml@refs/tags/v0.1.0-dev.5"},
		"a branch's run":         {other + ":0.1.0-dev.5", workflows + "release.yml@refs/heads/main"},
		"another version's run":  {other + ":0.1.0-dev.6", releaseRun},
		"a release's run":        {other + ":0.1.0", releaseRun},
		"a prefix of the run":    {other + ":0.1.0-dev.5", releaseRunFor("0.1.0-dev.55")},
	}
	for name, c := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := check(c.ref, c.workflowRef)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "garam-agent-operator/other:<12-hex commit hash> instead")
			assert.Contains(t, err.Error(), "ADR 0078")
		})
	}
}

func TestCheck_RefusesTagsNearTheReleaseFormsEvenInTheirOwnRun(t *testing.T) {
	for _, version := range []string{
		"1.2.3", "0.2.0", "0.1.1", "0.1.0-dev.0", "0.1.0-dev.01", "0.1.0-rc.0", "0.1.0-rc",
		"0.1.0-beta.1", "0.1.0-dev.1-x", "0.1.0-dev",
	} {
		_, err := check(other+":"+version, releaseRunFor(version))
		require.Error(t, err, version)
		assert.Contains(t, err.Error(), "ADR 0081", version)
	}

	// Control: the accepted form beside them is accepted in the identical setup.
	_, err := check(other+":0.1.0-dev.1", releaseRunFor("0.1.0-dev.1"))
	assert.NoError(t, err)
}

func TestCheck_AcceptsADevelopmentImageOnlyUnderItsTwelveHexHashAlone(t *testing.T) {
	for _, repository := range []string{other, control} {
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
		"a v in front":    control + ":v0.1.0-dev.5",
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
	_, err := check(other+":dev-abc123def456", releaseRun)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADR 0078")

	// Control: the same run accepts its own version.
	_, err = check(other+":0.1.0-dev.5", releaseRun)
	assert.NoError(t, err)
}

func TestCheck_AppliesNoRuleOutsideThePublishingRepository(t *testing.T) {
	// Tags the rule refuses: the scaffold's defaults, a repository whose name only starts with the
	// prefix, the prefix as a repository of its own, and another project's repositories.
	for _, ref := range []string{
		"controller:latest",
		"control:latest",
		registry + "garam-agent-operatorx/other:dev-1",
		registry + "garam-agent-operator:0.1.0",
		registry + "garam/test:0.1.0-dev.5",
		registry + "garam-dev/garam-test:dev-1",
	} {
		verdict, err := check(ref, "")
		require.NoError(t, err, ref)
		assert.Contains(t, verdict, "no rule applies", ref)
	}

	// Control: the same tags under the prefix are refused, so the path is what exempted them.
	for _, tag := range []string{"latest", "dev-1", "0.1.0-dev.5"} {
		_, err := check(other+":"+tag, "")
		assert.Error(t, err, tag)
	}
}

func TestRun_ReportsTheVerdictAndExitsByIt(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, 1, run([]string{other + ":0.1.0-rc.1"}, "", &out, &errOut))
	assert.Contains(t, errOut.String(), "ADR 0078")
	assert.Empty(t, out.String())

	out.Reset()
	errOut.Reset()
	assert.Equal(t, 0, run([]string{other + ":abc123def456"}, "", &out, &errOut))
	assert.Contains(t, out.String(), "a development image")
	assert.Empty(t, errOut.String())

	assert.Equal(t, 1, run(nil, "", &out, &errOut))
}
