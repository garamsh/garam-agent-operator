package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// launch is the tag of the launch release.
const launch = "v0.1.0"

func TestRun_PrintsTheVersionOfAReleaseTag(t *testing.T) {
	for tag, version := range map[string]string{
		"v0.1.0-dev.5": "0.1.0-dev.5",
		"v0.1.0-rc.2":  "0.1.0-rc.2",
		launch:         "0.1.0",
	} {
		var out, errOut bytes.Buffer
		assert.Equal(t, 0, run([]string{tag}, &out, &errOut), tag)
		assert.Equal(t, version+"\n", out.String())
		assert.Empty(t, errOut.String())
	}
}

func TestRun_RefusesWhatTheSchemeDoesNotName(t *testing.T) {
	for _, tag := range []string{
		"v0.2.0", "v1.0.0", "v0.1.0-dev.0", "v0.1.0-dev.01", "v0.1.0-rc", "v0.1.0-beta.1",
		"v0.1.0-dev.1-x", "0.1.0-dev.1", "main", "",
	} {
		var out, errOut bytes.Buffer
		assert.Equal(t, 1, run([]string{tag}, &out, &errOut), "%q", tag)
		assert.Empty(t, out.String())
		assert.Contains(t, errOut.String(), "nothing published")
	}
	var out, errOut bytes.Buffer
	assert.Equal(t, 1, run(nil, &out, &errOut))
	assert.Equal(t, 1, run([]string{launch, launch}, &out, &errOut))
}

// step is the part of a workflow step this test reads.
type step struct {
	Uses string `json:"uses"`
	Run  string `json:"run"`
}

func TestReleaseWorkflow_ChecksTheTagThroughThisCommandBeforeAnyCredential(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []step `json:"steps"`
		} `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow))
	steps := workflow.Jobs["publish"].Steps
	require.NotEmpty(t, steps)

	check, credential := -1, -1
	for i, s := range steps {
		if strings.Contains(s.Run, "go run ./hack/releasetag") && check < 0 {
			check = i
		}
		if strings.HasPrefix(s.Uses, "aws-actions/configure-aws-credentials@") && credential < 0 {
			credential = i
		}
	}
	require.GreaterOrEqual(t, check, 0, "release.yml does not run the check")
	require.GreaterOrEqual(t, credential, 0, "release.yml obtains no credential, so the order below proves nothing")
	assert.Less(t, check, credential, "the tag check must come before the credential step")

	// The tag reaches the command through the environment, not the command text.
	assert.NotContains(t, steps[check].Run, "github.ref_name")
	assert.Contains(t, steps[check].Run, `"$REF_NAME"`)
}
