// Command chartcheck checks the Helm charts under deploy/charts (ADR 0085, #424). Helm is only a template
// engine here: a chart renders gaon objects that `gaon apply` takes, so the check renders them and
// validates each one against gaon's API types rather than against Kubernetes'.
//
// For every chart (each directory under the charts root that holds a Chart.yaml) it
//
//   - renders every ci/*-values.yaml and validates each rendered object: the kind is one the chart may
//     render, the object decodes strictly, and gaon's own Validate accepts the MicroVM or Service it
//     describes. gaon's lib/api has no Deployment wrapper type, so the wrapper is decoded here and its
//     template is validated as the MicroVM every replica becomes;
//   - renders every ci/refused/<value>[@variant].yaml over ci/minimal-values.yaml and requires the render
//     to fail with a message that opens with <value>, so a refusal that came from another guard, or from
//     a typo, does not count;
//   - scans the chart's files outside ci/ for what a chart must not hold: a PEM block, an image digest, an
//     account id, an AWS host, a URL with a password.
//
// The gaon commit the objects are validated at is the one go.mod pins, because this program imports
// lib/api: moving the module moves the check, and the report prints the version it ran at.
//
// A count of zero is a failure, because a check that selected nothing has checked nothing.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/garamsh/gaon/lib/api"
	"sigs.k8s.io/yaml"
)

const (
	gaonModule = "github.com/garamsh/gaon/lib/api"
	// workloadsGroupVersion is the group of the Deployment kind, which lib/api has no type for.
	workloadsGroupVersion = "workloads.gaon.garam.sh/v1alpha1"
	kindDeployment        = "Deployment"
	kindService           = "Service"
)

// dockerfiles gives, for the command a rendered Deployment runs, the Dockerfile whose USER the object's
// `user` must equal: gaon starts the process as that user, and a key file is readable only by it.
var dockerfiles = map[string]string{
	"/control":                     "build/control.Dockerfile",
	"/usr/local/bin/gaon-operator": "build/operator.Dockerfile",
}

var (
	// image is the only reference form gaon starts.
	image = regexp.MustCompile(`^[^@:/]+(:[0-9]+)?(/[^@:/]+)*@sha256:[0-9a-f]{64}$`)
	// secretName matches the environment variables that hold a credential.
	secretName = regexp.MustCompile(`KEY|SECRET|PASSWORD|DATABASE_URL|_TOKEN$`)
	// user is a numeric uid:gid.
	user = regexp.MustCompile(`^[0-9]+:[0-9]+$`)
	// forbidden are the contents a chart outside ci/ must not hold, each with what it is.
	forbidden = []struct {
		pattern *regexp.Regexp
		what    string
	}{
		{regexp.MustCompile(`-----BEGIN `), "a PEM block"},
		{regexp.MustCompile(`sha256:[0-9a-f]{64}`), "an image digest"},
		{regexp.MustCompile(`\b[0-9]{12}\b`), "a 12-digit account id"},
		{regexp.MustCompile(`amazonaws\.com`), "an AWS host"},
		{regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`), "a URL with a password"},
	}
)

// report is what a run covered. It is printed whole, and any zero count fails the run.
type report struct {
	charts, renders, refusals, controls, scanned int
	objects                                      map[string]int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("chartcheck", flag.ContinueOnError)
	flags.SetOutput(errOut)
	helm := flags.String("helm", "helm", "the helm binary to render with")
	root := flags.String("charts", "deploy/charts", "the directory whose charts are checked")
	repo := flags.String("repo", ".", "the repository root, where the Dockerfiles named by build/ are")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	pin, err := gaonPin()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "chartcheck: %v\n", err)
		return 1
	}
	rep, failures := checkAll(*helm, *root, *repo)
	const summary = "chartcheck: validated against %s %s (go.mod's pin), charts selected as every directory " +
		"under %s holding a Chart.yaml: charts=%d renders=%d (ci/*-values.yaml) objects=%d %s " +
		"refusals=%d (ci/refused/*.yaml) controls=%d files scanned=%d\n"
	_, _ = fmt.Fprintf(out, summary,
		gaonModule, pin, *root, rep.charts, rep.renders, total(rep.objects), kinds(rep.objects), rep.refusals,
		rep.controls, rep.scanned)
	for name, n := range map[string]int{
		"charts": rep.charts, "renders": rep.renders, "objects": total(rep.objects),
		"refusals": rep.refusals, "controls": rep.controls, "files scanned": rep.scanned,
	} {
		if n == 0 {
			failures = append(failures, fmt.Errorf("the count of %s is zero: the check selected nothing to check", name))
		}
	}
	for _, f := range failures {
		_, _ = fmt.Fprintf(errOut, "chartcheck: %v\n", f)
	}
	if len(failures) > 0 {
		return 1
	}
	return 0
}

// gaonPin answers the version of lib/api this program was built with, which is go.mod's.
func gaonPin() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("the build information is missing, so the gaon pin cannot be read")
	}
	for _, dep := range info.Deps {
		if dep.Path == gaonModule {
			if dep.Replace != nil {
				return "", fmt.Errorf("%s is replaced by %s: the check would not run at go.mod's pin", gaonModule, dep.Replace.Path)
			}
			return dep.Version, nil
		}
	}
	return "", fmt.Errorf("%s is not among the dependencies of this program", gaonModule)
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func kinds(m map[string]int) string {
	parts := make([]string, 0, 2)
	for _, kind := range []string{kindDeployment, kindService} {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, m[kind]))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// checkAll checks every chart under root and answers what it covered and every failure it found.
func checkAll(helm, root, repo string) (report, []error) {
	rep := report{objects: map[string]int{}}
	charts, err := discover(root)
	if err != nil {
		return rep, []error{err}
	}
	var failures []error
	for _, chart := range charts {
		rep.charts++
		failures = append(failures, checkChart(helm, chart, repo, &rep)...)
	}
	return rep, failures
}

// discover answers every directory under root that holds a Chart.yaml, outside any ci/ directory.
func discover(root string) ([]string, error) {
	var charts []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "ci" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "Chart.yaml" {
			charts = append(charts, filepath.Dir(path))
		}
		return nil
	})
	return charts, err
}

func checkChart(helm, chart, repo string, rep *report) []error {
	var failures []error
	fail := func(format string, args ...any) { failures = append(failures, fmt.Errorf(chart+": "+format, args...)) }

	values, err := filepath.Glob(filepath.Join(chart, "ci", "*-values.yaml"))
	if err != nil {
		return []error{err}
	}
	minimal := filepath.Join(chart, "ci", "minimal-values.yaml")
	if !slices.Contains(values, minimal) {
		fail("%s is missing: the refused cases are rendered over it", minimal)
	}
	for _, file := range values {
		rendered, err := render(helm, chart, file)
		if err != nil {
			fail("%s does not render: %v", file, err)
			continue
		}
		rep.renders++
		docs := splitDocuments(rendered)
		if len(docs) == 0 {
			fail("%s renders no object", file)
		}
		for i, doc := range docs {
			kind, err := validateObject(doc, repo)
			if err != nil {
				fail("%s object %d: %v", file, i+1, err)
				continue
			}
			rep.objects[kind]++
		}
	}

	refused, err := filepath.Glob(filepath.Join(chart, "ci", "refused", "*.yaml"))
	if err != nil {
		return append(failures, err)
	}
	if len(refused) == 0 {
		fail("ci/refused holds no case: no refusal is checked")
	}
	if _, err := render(helm, chart, minimal); err == nil {
		rep.controls++
	}
	for _, file := range refused {
		if err := checkRefusal(helm, chart, minimal, file); err != nil {
			fail("%v", err)
			continue
		}
		rep.refusals++
	}

	scanned, err := scan(chart)
	rep.scanned += scanned
	if err != nil {
		fail("%v", err)
	}
	return failures
}

// errHelmNotRun marks a render that failed before helm ran, which is no refusal by a chart.
var errHelmNotRun = errors.New("helm did not run")

// render runs helm template over the chart with each values file in order, and answers its output.
func render(helm, chart string, valueFiles ...string) (string, error) {
	args := make([]string, 0, 3+2*len(valueFiles))
	args = append(args, "template", "check", chart)
	for _, f := range valueFiles {
		args = append(args, "-f", f)
	}
	var stderr strings.Builder
	cmd := exec.Command(helm, args...)
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			return "", fmt.Errorf("%w: %w", errHelmNotRun, err)
		}
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(stdout), nil
}

// splitDocuments cuts helm's output into its YAML documents, dropping the ones that are only comments.
func splitDocuments(rendered string) []string {
	var docs []string
	for doc := range strings.SplitSeq("\n"+rendered, "\n---") {
		var body []string
		for line := range strings.SplitSeq(doc, "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
				body = append(body, line)
			}
		}
		if len(body) > 0 {
			docs = append(docs, doc)
		}
	}
	return docs
}

// workload is the Deployment kind, which lib/api has no type for. Its template is a MicroVMSpec.
type workload struct {
	api.TypeMeta
	Metadata api.ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int32 `json:"replicas"`
		Strategy struct {
			Type string `json:"type"`
		} `json:"strategy"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template api.MicroVMSpec `json:"template"`
	} `json:"spec"`
}

// validateObject checks one rendered object and answers its kind. Only the kinds a chart of gaon objects
// renders are accepted: a Kubernetes kind, a Secret and a ConfigMap among the refused.
func validateObject(doc, repo string) (string, error) {
	var head api.TypeMeta
	if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
		return "", err
	}
	switch {
	case head.Kind == kindService && head.APIVersion == api.GroupVersion:
		var s api.Service
		if err := yaml.UnmarshalStrict([]byte(doc), &s); err != nil {
			return "", err
		}
		return kindService, s.Validate()
	case head.Kind == kindDeployment && head.APIVersion == workloadsGroupVersion:
		return kindDeployment, validateDeployment(doc, repo)
	}
	return "", fmt.Errorf("kind %q of %q is not one this chart may render: a gaon Deployment or Service",
		head.Kind, head.APIVersion)
}

func validateDeployment(doc, repo string) error {
	var d workload
	if err := yaml.UnmarshalStrict([]byte(doc), &d); err != nil {
		return err
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas < 1 {
		return errors.New("spec.replicas is missing or below 1")
	}
	if d.Spec.Strategy.Type != "Recreate" && d.Spec.Strategy.Type != "RollingUpdate" {
		return fmt.Errorf("spec.strategy.type is %q, want Recreate or RollingUpdate", d.Spec.Strategy.Type)
	}
	if len(d.Spec.Selector.MatchLabels) == 0 {
		return errors.New("spec.selector.matchLabels is empty")
	}
	tpl := d.Spec.Template
	// Every replica is a MicroVM named <deployment>-<index> that runs always, so that is what is validated.
	vm := api.MicroVM{
		APIVersion: api.GroupVersion,
		Kind:       api.KindMicroVM,
		Metadata:   api.ObjectMeta{Name: d.Metadata.Name + "-0"},
		Spec:       tpl,
	}
	vm.Spec.RunStrategy = api.RunAlways
	if err := vm.Validate(); err != nil {
		return err
	}
	if !image.MatchString(tpl.Image) {
		return fmt.Errorf("spec.template.image is not <repository>@sha256:<digest>")
	}
	if tpl.Resources.Requests.CPU == "" || tpl.Resources.Requests.Memory == "" {
		return errors.New("spec.template.resources.requests names no cpu or no memory")
	}
	if !user.MatchString(tpl.User) {
		return fmt.Errorf("spec.template.user is %q, want a numeric uid:gid", tpl.User)
	}
	if err := checkUser(tpl, repo); err != nil {
		return err
	}
	for _, e := range tpl.Env {
		// A name ending in _FILE holds the path of a file, which is not the credential.
		if secretName.MatchString(e.Name) && !strings.HasSuffix(e.Name, "_FILE") && e.Value != "" {
			return fmt.Errorf("env %s holds a value, and its name says it is a credential: use valueFrom", e.Name)
		}
	}
	for _, f := range tpl.Files {
		if f.Content != "" {
			return fmt.Errorf("file %s holds content: a chart names the object the file comes from", f.Path)
		}
		if f.ValueFrom == nil {
			return fmt.Errorf("file %s has no valueFrom", f.Path)
		}
	}
	return nil
}

// checkUser requires the template's user to be the USER of the Dockerfile that builds its command.
func checkUser(tpl api.MicroVMSpec, repo string) error {
	if len(tpl.Command) == 0 {
		return errors.New("spec.template.command is empty")
	}
	dockerfile, ok := dockerfiles[tpl.Command[0]]
	if !ok {
		return fmt.Errorf("command %q is not one this check knows a Dockerfile for", tpl.Command[0])
	}
	text, err := os.ReadFile(filepath.Join(repo, dockerfile))
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(text), "\n") {
		if rest, ok := strings.CutPrefix(line, "USER "); ok {
			if got := strings.TrimSpace(rest); got != tpl.User {
				return fmt.Errorf("user is %q, but %s's USER is %q", tpl.User, dockerfile, got)
			}
			return nil
		}
	}
	return fmt.Errorf("%s has no USER line", dockerfile)
}

// checkRefusal renders the refused case over the minimal values and requires the render to fail with a
// message that opens with the value the file is named for.
func checkRefusal(helm, chart, minimal, file string) error {
	value, _, _ := strings.Cut(strings.TrimSuffix(filepath.Base(file), ".yaml"), "@")
	if err := knownValue(chart, value); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	_, err := render(helm, chart, minimal, file)
	if err == nil {
		return fmt.Errorf("%s renders: the chart does not refuse %s", file, value)
	}
	if errors.Is(err, errHelmNotRun) {
		return fmt.Errorf("%s: %w", file, err)
	}
	guard := regexp.MustCompile(`execution error at \([^)]*\): ` + regexp.QuoteMeta(value) + `(\[[0-9]+\])?( |$)`)
	if !guard.MatchString(err.Error()) {
		return fmt.Errorf("%s is refused, but not by the guard of %s: %v", file, value, err)
	}
	return nil
}

// knownValue requires the dotted name to be a value the chart's values.yaml declares, so a case named for a
// misspelt value cannot pass by failing for another reason.
func knownValue(chart, value string) error {
	text, err := os.ReadFile(filepath.Join(chart, "values.yaml"))
	if err != nil {
		return err
	}
	var node any
	if err := yaml.Unmarshal(text, &node); err != nil {
		return err
	}
	for key := range strings.SplitSeq(value, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not a value of values.yaml", value)
		}
		if node, ok = m[key]; !ok {
			return fmt.Errorf("%s is not a value of values.yaml", value)
		}
	}
	return nil
}

// scan reads every file of the chart outside ci/ for what a chart must not hold, and answers how many it
// read.
func scan(chart string) (int, error) {
	n := 0
	var found []string
	err := filepath.WalkDir(chart, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "ci" {
				return filepath.SkipDir
			}
			return nil
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		for _, f := range forbidden {
			if f.pattern.Match(text) {
				found = append(found, fmt.Sprintf("%s holds %s", path, f.what))
			}
		}
		return nil
	})
	if err == nil && len(found) > 0 {
		err = errors.New(strings.Join(found, "; "))
	}
	return n, err
}
