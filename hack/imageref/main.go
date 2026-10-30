// Command imageref refuses an image reference ADR 0078's tag rule forbids a push of (#401). Under
// garam-agent-operator/ the tag decides: X.Y.Z only in .github/workflows/release.yml's run for that
// version, or the 12-hex commit hash alone. Every Makefile target that pushes, and release.yml before
// its push, runs it on the reference.
//
// It stops mistakes, not a determined pusher: GITHUB_WORKFLOW_REF is set by the Actions runner, and a
// local shell that exports it by hand passes. What stops the rest is IAM: which principals may write
// to garam-agent-operator/ is garamsh/aurora's.
//
// The reference is parsed with github.com/distribution/reference, the parser the docker CLI applies,
// rather than a second reading of its syntax.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/distribution/reference"
)

const (
	// publishingRepository holds both images, release and development alike (ADR 0078).
	publishingRepository = "garam-agent-operator/"
	// releaseWorkflow is GITHUB_WORKFLOW_REF's value in a release run, up to the tag's version.
	releaseWorkflow = "garamsh/garam-agent-operator/.github/workflows/release.yml@refs/tags/v"
)

var (
	// releaseVersion is the X.Y.Z a release image is tagged with, as release.yml's own check spells it.
	releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	// commitHash is the 12-hex abbreviated hash a development image is tagged with (ADR 0067).
	commitHash = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv("GITHUB_WORKFLOW_REF"), os.Stdout, os.Stderr))
}

// run checks the one reference in args, and answers the exit code: 0 when a push of it is allowed,
// 1 otherwise.
func run(args []string, workflowRef string, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		_, _ = fmt.Fprintln(errOut, "image reference: pass exactly one reference to check")
		return 1
	}
	verdict, err := check(args[0], workflowRef)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "image reference: %s: %v\n", args[0], err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "image reference: %s: %s\n", args[0], verdict)
	return 0
}

// check answers why a push of ref is allowed, or an error naming why it is refused. workflowRef is
// GITHUB_WORKFLOW_REF, empty outside GitHub Actions.
func check(ref, workflowRef string) (string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", fmt.Errorf("not an image reference: %v", err)
	}
	path := reference.Path(named)
	if !strings.HasPrefix(path, publishingRepository) {
		return "not under " + publishingRepository + ", so no rule applies", nil
	}
	tag := ""
	if tagged, ok := named.(reference.Tagged); ok {
		tag = tagged.Tag()
	}
	switch {
	case releaseVersion.MatchString(tag):
		if workflowRef == releaseWorkflow+tag {
			return "a release, pushed by the release workflow", nil
		}
		return "", fmt.Errorf("an X.Y.Z tag is pushed only by .github/workflows/release.yml in its run for "+
			"vX.Y.Z (ADR 0078); push a development image as %s:<12-hex commit hash> instead", path)
	case commitHash.MatchString(tag):
		return "a development image", nil
	default:
		return "", fmt.Errorf("a tag under %s is a release's X.Y.Z or a development image's 12-hex commit "+
			"hash alone, with no dev- prefix (ADR 0078), not %q; push %s:<12-hex commit hash> instead",
			publishingRepository, tag, path)
	}
}
