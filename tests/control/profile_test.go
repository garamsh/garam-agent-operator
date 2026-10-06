//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// published is what one run of the binary's publish-profile did: its exit code and what it wrote.
type published struct {
	code   int
	stdout string
	stderr string
}

// runPublishProfile runs the built binary's publish-profile over files, against the suite's store.
func runPublishProfile(t *testing.T, files ...string) published {
	t.Helper()
	args := []string{"publish-profile"}
	for _, f := range files {
		args = append(args, "--file", f)
	}
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		code = exited.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return published{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// profileFile writes fields as a profile file and returns its path. JSON is YAML, and it quotes
// the test-derived names whatever they hold.
func profileFile(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "profile.yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// runnable is a profile version publish-profile accepts, of organization's profile name.
func runnable(organization, profile string, version int) map[string]any {
	return map[string]any{"organization": organization, "name": profile, "version": version, "storageSize": "1Gi"}
}

// publishProfile publishes version 1 of a profile of organization, named for the test, through the
// binary's publish-profile, and returns its name.
func publishProfile(t *testing.T, organization string) string {
	t.Helper()
	profile := name(t, "profile")
	publishProfileNamed(t, organization, profile)
	return profile
}

// publishProfileNamed publishes version 1 of organization's profile through the binary.
func publishProfileNamed(t *testing.T, organization, profile string) {
	t.Helper()
	got := runPublishProfile(t, profileFile(t, runnable(organization, profile, 1)))
	require.Equal(t, 0, got.code, got.stderr)
}

// storedProfiles is every version of organization's profile in the store, by version.
func storedProfiles(t *testing.T, organization, profile string) map[int64]map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT version, settings FROM profiles WHERE organization = $1 AND name = $2", organization, profile)
	require.NoError(t, err)
	defer rows.Close()
	stored := map[int64]map[string]any{}
	for rows.Next() {
		var version int64
		var settings map[string]any
		require.NoError(t, rows.Scan(&version, &settings))
		stored[version] = settings
	}
	require.NoError(t, rows.Err())
	return stored
}

func TestPublishProfile_PublishesAVersionOnceThroughTheBinary(t *testing.T) {
	organization, profile := name(t, "organization"), name(t, "profile")
	fields := runnable(organization, profile, 1)
	fields["workspaceStorageSize"] = "5Gi"
	fields["resources"] = map[string]any{"requests": map[string]string{"cpu": "500m"}}

	got := runPublishProfile(t, profileFile(t, fields))
	require.Equal(t, 0, got.code, got.stderr)
	assert.Equal(t, fmt.Sprintf("published %s/%s version 1\n", organization, profile), got.stdout)
	stored := storedProfiles(t, organization, profile)
	require.Len(t, stored, 1)
	assert.Equal(t, "1Gi", stored[1]["storageSize"])
	assert.Equal(t, "5Gi", stored[1]["workspaceStorageSize"])

	// The same settings again, the storage size written in another form, publish nothing.
	fields["storageSize"] = "1024Mi"
	again := runPublishProfile(t, profileFile(t, fields))
	require.Equal(t, 0, again.code, again.stderr)
	assert.Equal(t, fmt.Sprintf("unchanged %s/%s version 1\n", organization, profile), again.stdout)
	assert.Equal(t, stored, storedProfiles(t, organization, profile))
}

func TestPublishProfile_RefusesSettingsNoWorkloadCouldRunWith(t *testing.T) {
	tests := []struct {
		name   string
		change func(fields map[string]any)
		code   int
	}{
		{"no storage size", func(f map[string]any) { delete(f, "storageSize") }, 1},
		{"a zero workspace size", func(f map[string]any) { f["workspaceStorageSize"] = "0" }, 1},
		{"a storage class no name", func(f map[string]any) { f["storageClassName"] = "Fast_SSD" }, 1},
		{"a request above its limit", func(f map[string]any) {
			f["resources"] = map[string]any{"requests": map[string]string{"cpu": "2"}, "limits": map[string]string{"cpu": "1"}}
		}, 1},
		{"a field a profile file does not name", func(f map[string]any) { f["image"] = "example.com/agent:v1" }, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			organization, profile := name(t, "organization"), name(t, "profile")
			fields := runnable(organization, profile, 1)
			tt.change(fields)
			got := runPublishProfile(t, profileFile(t, fields))
			assert.Equal(t, tt.code, got.code, got.stdout+got.stderr)
			assert.NotContains(t, got.stdout, "published")
			assert.Empty(t, storedProfiles(t, organization, profile), "a refused profile was stored")

			// Control: the same file without the change is published.
			control := runPublishProfile(t, profileFile(t, runnable(organization, profile, 1)))
			assert.Equal(t, 0, control.code, control.stderr)
		})
	}
}

func TestPublishProfile_RefusesOtherSettingsUnderAPublishedVersionAndAGap(t *testing.T) {
	organization, profile := name(t, "organization"), name(t, "profile")
	require.Equal(t, 0, runPublishProfile(t, profileFile(t, runnable(organization, profile, 1))).code)
	first := storedProfiles(t, organization, profile)

	changed := runnable(organization, profile, 1)
	changed["storageSize"] = "2Gi"
	got := runPublishProfile(t, profileFile(t, changed))
	assert.Equal(t, 1, got.code, got.stdout)
	assert.Equal(t, fmt.Sprintf("refused %s/%s version 1\n", organization, profile), got.stdout)
	assert.Contains(t, got.stderr, "published with other settings")
	assert.Equal(t, first, storedProfiles(t, organization, profile), "the published version changed")

	gap := runnable(organization, profile, 3)
	got = runPublishProfile(t, profileFile(t, gap))
	assert.Equal(t, 1, got.code, got.stdout)
	assert.Contains(t, got.stderr, "not the next one")

	// Control: the changed settings as the next version are published.
	changed["version"] = 2
	got = runPublishProfile(t, profileFile(t, changed))
	require.Equal(t, 0, got.code, got.stderr)
	assert.Equal(t, "2Gi", storedProfiles(t, organization, profile)[2]["storageSize"])
}

func TestPublishProfile_ConcurrentPublishersOfOneVersionStoreOne(t *testing.T) {
	for round := range 5 {
		organization, profile := name(t, "organization"), name(t, "profile")
		same := profileFile(t, runnable(organization, profile, 1))
		other := runnable(organization, profile, 1)
		other["storageSize"] = "2Gi"
		files := []string{same, same, profileFile(t, other)}

		results := make([]published, len(files))
		var wg sync.WaitGroup
		for i, f := range files {
			wg.Go(func() { results[i] = runPublishProfile(t, f) })
		}
		wg.Wait()

		stored := storedProfiles(t, organization, profile)
		require.Len(t, stored, 1, "round %d", round)
		outcomes := map[string]int{}
		for _, r := range results {
			outcome, _, _ := strings.Cut(r.stdout, " ")
			outcomes[outcome]++
			if r.code != 0 {
				assert.Contains(t, r.stderr, "published with other settings", "round %d: a loser was not told why", round)
			}
		}
		assert.Equal(t, 1, outcomes["published"], "round %d: %v", round, results)
		// The settings stored decide which runs agree with them: those are unchanged, the rest refused.
		if stored[1]["storageSize"] == "1Gi" {
			assert.Equal(t, map[string]int{"published": 1, "unchanged": 1, "refused": 1}, outcomes, "round %d", round)
		} else {
			assert.Equal(t, map[string]int{"published": 1, "refused": 2}, outcomes, "round %d", round)
		}
	}
}
