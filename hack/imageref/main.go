// Command imageref refuses an image reference that a push would put where ADR 0066 and ADR 0067
// forbid (#325): a repository under garam/ outside a release pushed by .github/workflows/release.yml,
// or a dev- tag under garam-dev/. Every Makefile target that pushes, and release.yml before its push,
// runs it on the reference.
//
// It stops mistakes, not a determined pusher: GITHUB_WORKFLOW_REF is set by the Actions runner, and a
// local shell that exports it by hand passes. What stops the rest is IAM: which principals may write
// to garam/ is garamsh/infra's.
//
// The reference is parsed with github.com/distribution/reference, the parser the docker CLI applies,
// rather than a second reading of its syntax.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/distribution/reference"
)

const (
	// releaseNamespace holds releases only (ADR 0066).
	releaseNamespace = "garam/"
	// developmentNamespace holds development images, tagged with the commit hash alone (ADR 0067).
	developmentNamespace = "garam-dev/"
	// releaseWorkflow is GITHUB_WORKFLOW_REF's value in a release run, up to the tag's version.
	releaseWorkflow = "garamsh/garam-agent-operator/.github/workflows/release.yml@refs/tags/v"
)

// releaseVersion is the X.Y.Z a release image is tagged with, as release.yml's own check spells it.
var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

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
	tag := ""
	if tagged, ok := named.(reference.Tagged); ok {
		tag = tagged.Tag()
	}
	switch {
	case strings.HasPrefix(path, releaseNamespace):
		if releaseVersion.MatchString(tag) && workflowRef == releaseWorkflow+tag {
			return "a release, pushed by the release workflow", nil
		}
		return "", fmt.Errorf("garam/ holds releases only, pushed by .github/workflows/release.yml "+
			"from a vX.Y.Z tag (ADR 0066); push a development image to %s%s:<12-hex commit hash> instead",
			developmentNamespace, strings.TrimPrefix(path, releaseNamespace))
	case strings.HasPrefix(path, developmentNamespace):
		if tag == "" {
			return "", errors.New("a development image under garam-dev/ is pushed under its 12-hex commit hash tag " +
				"(ADR 0067); name it")
		}
		if hash, ok := strings.CutPrefix(tag, "dev-"); ok {
			return "", fmt.Errorf("a development image under garam-dev/ carries the 12-hex commit hash tag alone, "+
				"with no dev- tag (ADR 0067); push %s:%s instead", path, hash)
		}
		return "a development image under garam-dev/", nil
	default:
		return "neither garam/ nor garam-dev/, so no rule applies", nil
	}
}
