// Command buildcontext fails when a file the module's Go source embeds is excluded from the Docker
// build context by .dockerignore, so an image build would stop at `go build` (#301). It reads the
// ignore file with moby/patternmatcher, the matcher BuildKit applies, rather than a second
// interpretation of its syntax.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// ignoreFile is the ignore file both images' builds read, at the root of their shared context.
const ignoreFile = ".dockerignore"

// pkg is what `go list -json` reports of a package that this check reads.
type pkg struct {
	ImportPath string
	Dir        string
	EmbedFiles []string
}

// embedded is one file a package embeds, by its path relative to the context root.
type embedded struct {
	path string
	by   string
}

func main() {
	os.Exit(run(".", os.Stdout, os.Stderr))
}

// run checks every file the packages under root embed against root's ignore file, and answers the
// exit code: 0 when each is in the build context, 1 otherwise.
func run(root string, out, errOut io.Writer) int {
	files, packages, err := embeddedFiles(root)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "build context: %v\n", err)
		return 1
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(errOut, "build context: no package embeds a file, so nothing was checked")
		return 1
	}
	raw, err := os.ReadFile(filepath.Join(root, ignoreFile))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "build context: %v\n", err)
		return 1
	}
	patterns, err := ignorefile.ReadAll(bytes.NewReader(raw))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "build context: read %s: %v\n", ignoreFile, err)
		return 1
	}
	missing, err := excluded(patterns, files)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "build context: %v\n", err)
		return 1
	}
	for _, f := range missing {
		_, _ = fmt.Fprintf(errOut, "build context: %s, embedded by %s, is excluded by %s: re-include it with a `!` line\n",
			f.path, f.by, ignoreFile)
	}
	if len(missing) > 0 {
		return 1
	}
	_, _ = fmt.Fprintf(out,
		"build context: %d embedded file(s), from %d package(s), all in the Docker build context %s leaves\n",
		len(files), packages, ignoreFile)
	return 0
}

// embeddedFiles is every file a package under root embeds, not counting test files, which never
// enter an image, and how many packages embed one.
func embeddedFiles(root string) ([]embedded, int, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	listed, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("go list: %v: %s", err, stderr.String())
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, err
	}
	var files []embedded
	packages := 0
	decoder := json.NewDecoder(bytes.NewReader(listed))
	for {
		var p pkg
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, 0, fmt.Errorf("decode go list: %v", err)
		}
		if len(p.EmbedFiles) > 0 {
			packages++
		}
		for _, f := range p.EmbedFiles {
			rel, err := filepath.Rel(absRoot, filepath.Join(p.Dir, f))
			if err != nil {
				return nil, 0, err
			}
			files = append(files, embedded{path: filepath.ToSlash(rel), by: p.ImportPath})
		}
	}
	return files, packages, nil
}

// excluded is each of files that patterns, an ignore file's lines in order, leave out of the
// context.
func excluded(patterns []string, files []embedded) ([]embedded, error) {
	matcher, err := patternmatcher.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %v", ignoreFile, err)
	}
	var missing []embedded
	for _, f := range files {
		ignored, err := matcher.MatchesOrParentMatches(f.path)
		if err != nil {
			return nil, fmt.Errorf("match %s: %v", f.path, err)
		}
		if ignored {
			missing = append(missing, f)
		}
	}
	return missing, nil
}
