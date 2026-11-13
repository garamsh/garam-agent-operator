// Command gaon-render renders one agent the way the operator does and
// writes what a container needs to run the same image: the command, an
// env file and the credential files. It is for tests/gaon/guest-image.sh, so the run
// starts from the rendered MicroVM and not from a copy of it. Every
// valueFrom is resolved here from the rendered Secret or from the one
// file the deployment's model Secret stands for; a source it cannot
// resolve fails the run.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gaon-render:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var (
		image, agentGRN, machineURL, controlURL string
		provider, baseURL, modelName, egoText   string
		modelRef, modelFile, rootFile, outDir   string
		epoch                                   int64
	)
	f := flag.NewFlagSet("gaon-render", flag.ContinueOnError)
	f.StringVar(&image, "image", "", "the guest image")
	f.StringVar(&agentGRN, "agent", "", "the agent's GRN")
	f.Int64Var(&epoch, "epoch", 1, "the assignment epoch")
	f.StringVar(&machineURL, "machine-url", "", "garam's machine listener, as the guest reaches it")
	f.StringVar(&controlURL, "control-url", "", "the control service, as the guest reaches it")
	f.StringVar(&provider, "provider", "", "the model provider; none renders an agent with no model")
	f.StringVar(&baseURL, "model-url", "", "the model's endpoint")
	f.StringVar(&modelName, "model-name", "", "the model's name")
	f.StringVar(&modelRef, "model-ref", "", "the model key's apiKeyRef, <secret-name>/<key>")
	f.StringVar(&egoText, "ego", "", "the agent's ego")
	f.StringVar(&modelFile, "model-key-file", "", "a file holding the model key, standing for that Secret")
	f.StringVar(&rootFile, "root-file", "", "a certificate file used for both trust roots")
	f.StringVar(&outDir, "out", "", "an empty directory to write into")
	if err := f.Parse(args); err != nil {
		return err
	}
	g, err := grn.Parse(agentGRN)
	if err != nil {
		return errorx.Wrap(err, "agent", errorx.KindInvalidArgument)
	}
	root, err := os.ReadFile(rootFile)
	if err != nil {
		return errorx.Wrap(err, "read root file", errorx.KindInvalidArgument)
	}
	opts := agent.Options{
		Image: image, GaramRoot: root, ControlRoot: root,
		MachineURL: machineURL, ControlURL: controlURL,
	}
	conf := agent.Configuration{Ego: egoText}
	if provider != "" {
		conf.Model = &agent.Model{Provider: provider, BaseURL: baseURL, Name: modelName, APIKeyRef: modelRef}
	}
	modelSecret, modelKey, _ := strings.Cut(modelRef, "/")
	creds := agent.Credentials{"agent-token": []byte("demo-token"), "control-token": []byte("demo-token")}
	// The model Secret stands for the one the deployment labels as the
	// agent's organization's model key.
	models := []api.Secret{{Metadata: api.ObjectMeta{Name: modelSecret, Labels: agent.ModelKeyLabels(g)}}}
	secret, microvm, err := agent.Render(opts, agent.Desired{
		GRN: g, Epoch: epoch, Revision: "1", MemoryStorage: "1Gi", WorkspaceStorage: "1Gi",
		Configuration: conf,
	}, creds, models)
	if err != nil {
		return err
	}
	resolve := func(ref *api.KeyReference) ([]byte, error) {
		switch {
		case ref == nil:
			return nil, errorx.New(errorx.KindInvalidArgument, "a valueFrom names no Secret key")
		case ref.Name == secret.Metadata.Name:
			if v, ok := secret.Data[ref.Key]; ok {
				return v, nil
			}
		case conf.Model != nil && ref.Name == modelSecret && ref.Key == modelKey:
			return os.ReadFile(modelFile)
		}
		return nil, errorx.New(errorx.KindInvalidArgument, "a valueFrom names a Secret key nothing here stands for")
	}
	return write(outDir, microvm, resolve)
}

// write materializes m into dir: entrypoint, env.list and files/.
func write(dir string, m *api.MicroVM, resolve func(*api.KeyReference) ([]byte, error)) error {
	if len(m.Spec.Command) != 1 || len(m.Spec.Args) != 0 {
		return errorx.New(errorx.KindInvalidArgument,
			"guest-image.sh carries one command word and no arguments, and the MicroVM renders more")
	}
	if err := os.WriteFile(filepath.Join(dir, "entrypoint"), []byte(m.Spec.Command[0]), 0o600); err != nil {
		return errorx.Wrap(err, "write entrypoint")
	}
	var env strings.Builder
	for _, e := range m.Spec.Env {
		value := e.Value
		if e.ValueFrom != nil {
			v, err := resolve(e.ValueFrom.SecretKeyRef)
			if err != nil {
				return errorx.Wrap(err, "env "+e.Name)
			}
			value = string(v)
		}
		if strings.ContainsAny(value, "\n") {
			return errorx.New(errorx.KindInvalidArgument, "env "+e.Name+" holds a newline, which an env file cannot carry")
		}
		fmt.Fprintf(&env, "%s=%s\n", e.Name, value)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.list"), []byte(env.String()), 0o600); err != nil {
		return errorx.Wrap(err, "write env")
	}
	files := filepath.Join(dir, "files")
	if err := os.Mkdir(files, 0o700); err != nil {
		return errorx.Wrap(err, "make files")
	}
	var modes []string
	for _, file := range m.Spec.Files {
		if file.Mode == nil || file.Owner == nil || filepath.Dir(file.Path) != "/run/garam" {
			return errorx.New(errorx.KindInvalidArgument,
				"file "+file.Path+" is not a file of /run/garam with a mode and an owner")
		}
		v := []byte(file.Content)
		if file.ValueFrom != nil {
			var err error
			if v, err = resolve(file.ValueFrom.SecretKeyRef); err != nil {
				return errorx.Wrap(err, "file "+file.Path)
			}
		}
		if err := os.WriteFile(filepath.Join(files, filepath.Base(file.Path)), v, 0o600); err != nil {
			return errorx.Wrap(err, "write file")
		}
		modes = append(modes, strconv.FormatInt(int64(*file.Mode), 8)+":"+
			strconv.FormatInt(file.Owner.UID, 10)+":"+strconv.FormatInt(file.Owner.GID, 10))
	}
	return os.WriteFile(filepath.Join(dir, "modes"), []byte(strings.Join(uniq(modes), "\n")+"\n"), 0o600)
}

// uniq returns the distinct values in order, which is how guest-image.sh learns
// that every file carries one mode and one owner.
func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
