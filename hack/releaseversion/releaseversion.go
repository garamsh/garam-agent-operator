// Package releaseversion decides which versions a release of this project may carry (ADR 0081): until
// launch 0.1.0-dev.N or 0.1.0-rc.N, and at launch 0.1.0. It is the one place the three forms are
// spelled; .github/workflows/release.yml (through hack/releasetag) and hack/imageref both read it.
package releaseversion

import (
	"fmt"
	"regexp"
	"strings"
)

// form is a release's version: 0.1.0, or 0.1.0 with a -dev.N or -rc.N suffix where N counts from 1,
// written without a leading zero.
var form = regexp.MustCompile(`^0\.1\.0(-(dev|rc)\.[1-9][0-9]*)?$`)

// Valid reports whether version is one of the three forms a release carries, and so one of the three
// an image of that release is tagged with.
func Valid(version string) bool {
	return form.MatchString(version)
}

// FromTag answers the version a git tag names, which is the tag without its leading v, or an error
// naming why the tag is not a release.
func FromTag(tag string) (string, error) {
	version, ok := strings.CutPrefix(tag, "v")
	if !ok || !Valid(version) {
		return "", fmt.Errorf("tag %q is not v0.1.0-dev.N, v0.1.0-rc.N or v0.1.0, where N counts from 1 "+
			"and has no leading zero (ADR 0081)", tag)
	}
	return version, nil
}
