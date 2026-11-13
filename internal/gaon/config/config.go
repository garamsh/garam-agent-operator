// Package config reads the operator's settings from its environment.
// It holds addresses and file paths only: a credential is a file the
// deployment mounts, never a value in the environment.
package config

import (
	"regexp"
	"strings"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam/lib/errorx"
)

// Config is what the operator is deployed with (ADR-0151 §6).
type Config struct {
	// GaonURL, GaonCAFile, GaonClientCertFile and GaonClientKeyFile
	// reach gaon's resource API, which authenticates the operator by a
	// client certificate.
	GaonURL            string
	GaonCAFile         string
	GaonClientCertFile string
	GaonClientKeyFile  string
	// GaramMachineURL and GaramRootFile reach garam's machine listener.
	GaramMachineURL string
	GaramRootFile   string
	// GaramTokenURL is the token URL the key issuance answered, the
	// assertion's audience.
	GaramTokenURL string
	// ControlURL and ControlRootFile reach the control service, and
	// ControlGRN is its operator GRN, the audience of the tokens it is
	// shown.
	ControlURL      string
	ControlRootFile string
	ControlGRN      string
	// OperatorKeyFile and OperatorClientID are the operator's own
	// identity for the client-assertion exchange. The client id is the
	// manager's operator GRN, which the control service is also told as
	// the controller.
	OperatorKeyFile  string
	OperatorClientID string
	// AgentImage is the composed guest image of this release, as
	// <repository>@sha256:<64 hex digits>, the only form gaon's Node
	// Agent starts.
	AgentImage string
	// GuestMachineURL and GuestControlURL are the addresses the adapter
	// in the guest reaches garam and the control service at. Each
	// defaults to the operator's own address for the same service.
	GuestMachineURL string
	GuestControlURL string
	// AgentCPU and AgentMemory are the MicroVM's requests and limits,
	// as gaon's quantities ("1", "1Gi"). gaon's Node Agent refuses a
	// MicroVM that names no cpu or no memory, so both are required and
	// no figure is published here.
	AgentCPU    string
	AgentMemory string
	// HealthAddr is where the probes listen. It defaults to
	// DefaultHealthAddr.
	HealthAddr string
}

// DefaultHealthAddr is the health listener's address when HEALTH_ADDR
// is unset.
const DefaultHealthAddr = ":8081"

type setting struct {
	name string
	dest func(*Config) *string
}

var settings = []setting{
	{"GAON_API_URL", func(c *Config) *string { return &c.GaonURL }},
	{"GAON_CA_FILE", func(c *Config) *string { return &c.GaonCAFile }},
	{"GAON_CLIENT_CERT_FILE", func(c *Config) *string { return &c.GaonClientCertFile }},
	{"GAON_CLIENT_KEY_FILE", func(c *Config) *string { return &c.GaonClientKeyFile }},
	{"GARAM_MACHINE_URL", func(c *Config) *string { return &c.GaramMachineURL }},
	{"GARAM_ROOT_FILE", func(c *Config) *string { return &c.GaramRootFile }},
	{"GARAM_TOKEN_URL", func(c *Config) *string { return &c.GaramTokenURL }},
	{"CONTROL_URL", func(c *Config) *string { return &c.ControlURL }},
	{"CONTROL_ROOT_FILE", func(c *Config) *string { return &c.ControlRootFile }},
	{"CONTROL_GRN", func(c *Config) *string { return &c.ControlGRN }},
	{"OPERATOR_KEY_FILE", func(c *Config) *string { return &c.OperatorKeyFile }},
	{"OPERATOR_CLIENT_ID", func(c *Config) *string { return &c.OperatorClientID }},
	{"AGENT_IMAGE", func(c *Config) *string { return &c.AgentImage }},
	{"AGENT_CPU", func(c *Config) *string { return &c.AgentCPU }},
	{"AGENT_MEMORY", func(c *Config) *string { return &c.AgentMemory }},
}

// imagePattern is the one gaon's Node Agent applies
// (node-agent/internal/microvms), so an image it would refuse is
// refused here at load.
var imagePattern = regexp.MustCompile(
	`^([^@:]+(?::[0-9]+)?(?:/[^@:]+)*)@(sha256:[0-9a-f]{64})$`)

// Load reads every setting through getenv and names each one that is
// missing or malformed. The error carries variable names, never values.
func Load(getenv func(string) string) (Config, error) {
	var c Config
	var missing []string
	for _, s := range settings {
		v := strings.TrimSpace(getenv(s.name))
		if v == "" {
			missing = append(missing, s.name)
			continue
		}
		*s.dest(&c) = v
	}
	if len(missing) > 0 {
		return Config{}, errorx.New(errorx.KindInvalidArgument,
			"missing settings: "+strings.Join(missing, ", "))
	}
	if bad := malformed(c); len(bad) > 0 {
		return Config{}, errorx.New(errorx.KindInvalidArgument,
			"malformed settings: "+strings.Join(bad, ", "))
	}
	c.GuestMachineURL = optional(getenv, "GUEST_MACHINE_URL", c.GaramMachineURL)
	c.GuestControlURL = optional(getenv, "GUEST_CONTROL_URL", c.ControlURL)
	c.HealthAddr = strings.TrimSpace(getenv("HEALTH_ADDR"))
	if c.HealthAddr == "" {
		c.HealthAddr = DefaultHealthAddr
	}
	return c, nil
}

// malformed names each setting whose value gaon would refuse: an image
// not pinned by digest, a resource that is not a quantity.
func malformed(c Config) []string {
	var bad []string
	if !imagePattern.MatchString(c.AgentImage) {
		bad = append(bad, "AGENT_IMAGE")
	}
	if _, err := api.ParseQuantity(c.AgentCPU); err != nil {
		bad = append(bad, "AGENT_CPU")
	}
	if _, err := api.ParseQuantity(c.AgentMemory); err != nil {
		bad = append(bad, "AGENT_MEMORY")
	}
	return bad
}

// optional returns the setting name, or fallback where it is unset.
func optional(getenv func(string) string, name, fallback string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return fallback
}
