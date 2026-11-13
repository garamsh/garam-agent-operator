package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/garamsh/gaon/lib/api"
	"go.yaml.in/yaml/v3"

	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// Configuration is what the feed's `configuration` holds for the
// runtime: the model and its embeddings endpoint, the tool pins and
// the ego. It is rendered as garamsh/garam-agent-operator renders it
// for a Pod, at ac7c9f3c40b9611f6e2bdf1eba62c14e145b4aec.
type Configuration struct {
	// Model is nil for an agent configured with none.
	Model *Model
	// Ego is the organisation's own opening of the agent's
	// instructions, empty where there is none. garam's reply
	// instruction is not part of it.
	Ego string
	// Tools are the tool pins, opaque to the operator.
	Tools map[string]string
}

// Model is the chat model of an agent. APIKeyRef names the Secret and
// the key holding the endpoint's key as "<secret-name>/<key>"; it is
// never the key.
type Model struct {
	Provider  string
	BaseURL   string
	Name      string
	APIKeyRef string
	// Embedding is the embeddings endpoint the memory is recalled
	// with, nil where there is none.
	Embedding *Embedding
}

// Embedding is an embeddings endpoint. APIKeyRef is as the model's, and
// empty where the endpoint takes no key.
type Embedding struct {
	BaseURL   string
	Name      string
	APIKeyRef string
}

// The variables the runtime reads each key from. They are the
// operator's names, which the configuration file gives the runtime
// under api-key-env. They sit outside the SHERLOCK_ prefix, so they
// bind to no setting of the runtime's.
const (
	modelKeyVariable     = "MODEL_API_KEY"
	embeddingKeyVariable = "EMBEDDING_API_KEY"
	// mockProvider is the provider that runs offline, with no
	// embeddings endpoint.
	mockProvider = "mock"
)

// The runtime's configuration file (sherlock v0.10.0 internal/config),
// marshalled rather than assembled, so that a foreign string changes
// what the file means only where somebody wrote its syntax by hand.
type (
	runtimeConfig struct {
		Tools     *runtimeTools     `yaml:"tools,omitempty"`
		Model     *runtimeModel     `yaml:"model,omitempty"`
		Embedding *runtimeEmbedding `yaml:"embedding,omitempty"`
	}
	runtimeTools struct {
		Pins map[string]string `yaml:"pins"`
	}
	runtimeModel struct {
		Provider  string `yaml:"provider"`
		BaseURL   string `yaml:"base-url"`
		Model     string `yaml:"model"`
		APIKeyEnv string `yaml:"api-key-env"`
	}
	runtimeEmbedding struct {
		BaseURL   string `yaml:"base-url"`
		Model     string `yaml:"model"`
		APIKeyEnv string `yaml:"api-key-env,omitempty"`
	}
)

// runtimeSettings is what an agent's configuration becomes in the
// MicroVM: its files, its environment, and the variables the runtime
// alone is given.
type runtimeSettings struct {
	files []api.MicroVMFile
	env   []api.EnvVar
}

// modelKeys are the names of the Secrets a reference of one agent may
// name: those labelled as model keys of the agent's own organization.
type modelKeys map[string]bool

// modelKeysFor keeps of secrets the ones that carry both labels
// ModelKeyLabels gives for g. The listing's selector already asks for
// the role; the check here keeps a Secret of another organization out
// whatever the client was built with.
func modelKeysFor(g grn.GRN, secrets []api.Secret) modelKeys {
	want := ModelKeyLabels(g)
	out := modelKeys{}
	for i := range secrets {
		l := secrets[i].Metadata.Labels
		if l[labelRole] == want[labelRole] && l[labelOrganization] == want[labelOrganization] {
			out[secrets[i].Metadata.Name] = true
		}
	}
	return out
}

// modelKeys returns the Secrets d's configuration may name. gaon has no
// namespaces, so a reference to a Secret of no organization, or of
// another, would put its value in this agent's environment. The list
// returns the Secrets' data, which is dropped at once, never used and
// never logged.
func (s *service) modelKeys(ctx context.Context, d Desired, snap *snapshot) (modelKeys, error) {
	if d.Configuration.Model == nil || s.opts.ModelKeys == nil {
		return nil, nil
	}
	if !snap.keysRead {
		list, err := s.opts.ModelKeys.ListSecrets(ctx)
		if err != nil {
			return nil, errorx.Wrap(err, "list model-key secrets")
		}
		for i := range list.Items {
			list.Items[i].Data = nil
		}
		snap.keys, snap.keysRead = list.Items, true
	}
	return modelKeysFor(d.GRN, snap.keys), nil
}

// runtimeFor renders c. A key is an `env` entry sourced from the
// Secret c names, which must be one of keys, and the operator never
// reads it; a file's content is inline and holds no secret, because
// nothing of c is one.
func runtimeFor(c Configuration, keys modelKeys) (runtimeSettings, error) {
	var out runtimeSettings
	var cfg runtimeConfig
	var keyVars []string
	keyEnv := func(variable, ref string) error {
		secret, key, err := parseSecretRef(ref)
		if err != nil {
			return err
		}
		if !keys[secret] {
			return errorx.New(errorx.KindFailedPrecondition,
				"the Secret of reference "+ref+" does not exist or is not labelled as a model key of this agent's organization")
		}
		keyVars = append(keyVars, variable)
		out.env = append(out.env, api.EnvVar{Name: variable, ValueFrom: &api.ValueSource{
			SecretKeyRef: &api.KeyReference{Name: secret, Key: key},
		}})
		return nil
	}
	if m := c.Model; m != nil {
		if m.Provider == "" || m.BaseURL == "" || m.Name == "" {
			return out, errorx.New(errorx.KindInvalidArgument, "a model is missing its provider, base URL or name")
		}
		if err := keyEnv(modelKeyVariable, m.APIKeyRef); err != nil {
			return out, errorx.Wrap(err, "model API key reference")
		}
		cfg.Model = &runtimeModel{Provider: m.Provider, BaseURL: m.BaseURL, Model: m.Name, APIKeyEnv: modelKeyVariable}
		switch e := m.Embedding; {
		case e == nil && m.Provider != mockProvider:
			return out, errorx.New(errorx.KindInvalidArgument, "a model other than mock has no embedding")
		case e != nil:
			if e.BaseURL == "" || e.Name == "" {
				return out, errorx.New(errorx.KindInvalidArgument, "an embedding is missing its base URL or name")
			}
			cfg.Embedding = &runtimeEmbedding{BaseURL: e.BaseURL, Model: e.Name}
			if e.APIKeyRef != "" {
				if err := keyEnv(embeddingKeyVariable, e.APIKeyRef); err != nil {
					return out, errorx.Wrap(err, "embedding API key reference")
				}
				cfg.Embedding.APIKeyEnv = embeddingKeyVariable
			}
		}
	}
	if len(c.Tools) > 0 {
		cfg.Tools = &runtimeTools{Pins: c.Tools}
	}

	if cfg != (runtimeConfig{}) {
		text, err := yaml.Marshal(cfg)
		if err != nil {
			return out, errorx.Wrap(err, "render the runtime configuration", errorx.KindInternal)
		}
		out.files = append(out.files, runtimeFile(guest.RuntimeConfigFile, string(text)))
		out.env = append(out.env, api.EnvVar{Name: guest.EnvRuntimeConfig, Value: guest.RuntimeConfigFile})
	}
	if c.Ego != "" {
		out.files = append(out.files, runtimeFile(guest.RuntimeEgoFile, c.Ego))
		out.env = append(out.env, api.EnvVar{Name: guest.EnvRuntimeEgo, Value: guest.RuntimeEgoFile})
	}
	if len(keyVars) > 0 {
		out.env = append(out.env, api.EnvVar{Name: guest.EnvRuntimeEnv, Value: strings.Join(keyVars, ",")})
	}
	return out, nil
}

// runtimeFile is a file of content that UID alone can read.
func runtimeFile(path, content string) api.MicroVMFile {
	mode, owner := int32(fileMode), api.FileOwner{UID: credentialUID, GID: credentialUID}
	return api.MicroVMFile{Path: path, Mode: &mode, Owner: &owner, Content: content}
}

var (
	// dnsSubdomain and dataKey are the forms of a Secret's name and of
	// a key in it, as the Kubernetes operator states them for a
	// reference.
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	dataKey      = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
	// ownName is the form of the name the operator gives an agent's
	// Secret: gaon's namespace is flat, so a reference to one would
	// read another agent's credentials.
	ownName = regexp.MustCompile(`^agent-[0-9a-f]{16}$`)
)

const maxRefName = 253

// parseSecretRef splits "<secret-name>/<key>", which neither half can
// hold a "/" in. It refuses a name of the operator's own objects.
func parseSecretRef(ref string) (secret, key string, err error) {
	secret, key, _ = strings.Cut(ref, "/")
	switch {
	case len(secret) > maxRefName || !dnsSubdomain.MatchString(secret):
		return "", "", errorx.New(errorx.KindInvalidArgument, "reference is not <secret-name>/<key>: the name is not a DNS subdomain")
	case len(key) > maxRefName || !dataKey.MatchString(key) || key == "." || key == "..":
		return "", "", errorx.New(errorx.KindInvalidArgument, "reference is not <secret-name>/<key>: the key is not a Secret key")
	case ownName.MatchString(secret):
		return "", "", errorx.New(errorx.KindInvalidArgument, "reference names a Secret the operator keeps for an agent")
	}
	return secret, key, nil
}
