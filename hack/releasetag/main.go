// Command releasetag answers the image version a release's git tag names, and refuses a tag that is
// not a release (ADR 0081). .github/workflows/release.yml runs it before any credential, so a wrong
// tag costs nothing: both repositories are immutable and a wrong name is permanent. The tag reaches it
// as an argument, never in command text.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/garamsh/garam-agent-operator/hack/releaseversion"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run answers the exit code: 0 and the version on out when the one tag in args is a release, 1 and the
// reason on errOut otherwise.
func run(args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errOut, "release tag: pass exactly one tag to check")
		return 1
	}
	version, err := releaseversion.FromTag(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "release tag: %v: nothing published\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(out, version)
	return 0
}
