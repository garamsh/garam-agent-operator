package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The seed's script runs in the copy image, which envtest does not run, so these
// run it here, with /bin/sh, against directories standing in for the two claims.

// seededContent is what the workspace's file holds when the seed first copies it.
const seededContent = "first"

// seedPaths are where one run of the seed reads and writes, under a test's own
// directory.
type seedPaths struct {
	from, to, marker string
}

func newSeedPaths(t *testing.T) seedPaths {
	t.Helper()

	root := t.TempDir()
	paths := seedPaths{
		from:   filepath.Join(root, "state", workspaceDirName),
		to:     filepath.Join(root, "workspace", workspaceDirName),
		marker: filepath.Join(root, "workspace", seededMarker),
	}
	if err := os.MkdirAll(filepath.Dir(paths.to), 0o750); err != nil {
		t.Fatal(err)
	}

	return paths
}

// runSeed runs the seed's command against paths, with pathPrefix searched before
// the system's PATH.
func runSeed(t *testing.T, paths seedPaths, pathPrefix string) {
	t.Helper()

	command := seedWorkspaceCommand(paths.from, paths.to, paths.marker)
	run := exec.Command(command[0], command[1:]...) //nolint:gosec // the test's own paths
	run.Env = append(os.Environ(), "PATH="+pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the seed failed: %v: %s", err, output)
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(name) //nolint:gosec // the test's own paths
	if err != nil {
		t.Fatal(err)
	}

	return string(content)
}

func TestSeedCopiesTheWorkspaceOnceAndLeavesTheSourceInPlace(t *testing.T) {
	paths := newSeedPaths(t)
	writeFile(t, filepath.Join(paths.from, "notes.md"), seededContent)
	writeFile(t, filepath.Join(paths.from, "repo", "main.go"), "package main")

	// The control: the first run copies.
	runSeed(t, paths, "")
	if got := readFile(t, filepath.Join(paths.to, "repo", "main.go")); got != "package main" {
		t.Fatalf("the nested file was copied as %q", got)
	}
	if got := readFile(t, filepath.Join(paths.to, "notes.md")); got != seededContent {
		t.Fatalf("the file was copied as %q", got)
	}
	if _, err := os.Stat(paths.marker); err != nil {
		t.Fatalf("no marker after the copy: %v", err)
	}
	if got := readFile(t, filepath.Join(paths.from, "notes.md")); got != seededContent {
		t.Fatalf("the source is %q after a copy, which leaves it in place", got)
	}

	// A second run copies nothing: the workspace is its own from the first.
	writeFile(t, filepath.Join(paths.from, "notes.md"), "changed on the state claim")
	writeFile(t, filepath.Join(paths.from, "later.md"), "later")
	runSeed(t, paths, "")
	if got := readFile(t, filepath.Join(paths.to, "notes.md")); got != seededContent {
		t.Fatalf("a second run copied again: %q", got)
	}
	if _, err := os.Stat(filepath.Join(paths.to, "later.md")); !os.IsNotExist(err) {
		t.Fatalf("a second run copied a file the first did not see: %v", err)
	}
}

func TestSeedOfAStateClaimWithNoWorkspaceMarksItSeeded(t *testing.T) {
	paths := newSeedPaths(t)

	runSeed(t, paths, "")
	if _, err := os.Stat(paths.marker); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	if _, err := os.Stat(paths.to); !os.IsNotExist(err) {
		t.Fatalf("a workspace was made from nothing: %v", err)
	}
}

func TestSeedMakesTheCopyDurableBeforeItWritesTheMarker(t *testing.T) {
	paths := newSeedPaths(t)
	copied := filepath.Join(paths.to, "notes.md")
	writeFile(t, filepath.Join(paths.from, "notes.md"), seededContent)

	// A sync that records, each time it is called, whether the copy and the
	// marker exist yet.
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "syncs")
	writeFile(t, filepath.Join(bin, "sync"), "#!/bin/sh\n"+
		"c=absent; [ -e "+copied+" ] && c=present\n"+
		"m=absent; [ -e "+paths.marker+" ] && m=present\n"+
		"echo \"copy=$c marker=$m\" >> "+log+"\n")
	if err := os.Chmod(filepath.Join(bin, "sync"), 0o700); err != nil { //nolint:gosec // an executable stand-in
		t.Fatal(err)
	}

	runSeed(t, paths, bin)

	syncs := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	want := []string{"copy=present marker=absent", "copy=present marker=present"}
	if strings.Join(syncs, "|") != strings.Join(want, "|") {
		t.Fatalf("the syncs saw %q, and want the copy synced before the marker exists and the marker synced after: %q",
			syncs, want)
	}
}
