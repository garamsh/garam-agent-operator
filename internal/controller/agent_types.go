package controller

import (
	"fmt"
	"reflect"

	"sigs.k8s.io/yaml"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// agentTypeDescriptor carries every name the Pod builder writes that belongs to
// one agent binary: the paths its containers mount, the variables they read, and
// the config file it resolves. The controller looks the descriptor up by
// Agent.Spec.Type once per reconcile and builds the workload through it.
//
// Every field is required. A type whose descriptor leaves any field unset is
// refused on reconcile with ReasonTypeUnimplemented, so a half-filled entry
// refuses the workload rather than building it with a missing name.
type agentTypeDescriptor struct {
	// credentialsMountPath is where the agent reads the copy of its credential.
	credentialsMountPath string

	// credentialsSecretMountPath is where the kubelet projects the credential's
	// Secret, and only the init container that copies it mounts it.
	credentialsSecretMountPath string

	// stateMountPath is where the agent's state volume is mounted, in the agent
	// container and in its workspace.
	stateMountPath string

	// memoryPathVariable is the variable the agent reads its memory store's path
	// from, and memoryFile is that path relative to stateMountPath.
	memoryPathVariable string
	memoryFile         string

	// configMountPath is the configuration directory the agent is given and the
	// one its config file is found under.
	configMountPath string

	// configHomeVariable is the variable the agent reads configMountPath from.
	configHomeVariable string

	// configFile is the config file's path relative to configMountPath.
	configFile string

	// renderConfig is the text of the config file an Agent's spec becomes, in
	// the agent's own setting names.
	renderConfig func(agentv1alpha1.AgentSpec) (string, error)

	// workspaceAddress is where the workspace listens and the agent dials.
	workspaceAddress string

	// listenAddressVariable is the workspace's own listen address;
	// workspaceAddressVariable is the agent's address for the workspace. They
	// are the two ends of one link.
	listenAddressVariable    string
	workspaceAddressVariable string

	// workspaceDirVariable names the directory the workspace serves.
	workspaceDirVariable string

	// execUserVariable names the uid the workspace runs exec children under.
	execUserVariable string
}

// agentTypeSherlock is the descriptor for type=sherlock (and the unset default).
// Every name in it is either sherlock's or chosen by this operator for sherlock;
// sherlock's deployment contract is
// sherlock@8218189:docs/architecture/deployment.md.
var agentTypeSherlock = agentTypeDescriptor{
	credentialsMountPath:       "/run/sherlock/credentials",
	credentialsSecretMountPath: "/etc/sherlock/credentials",
	stateMountPath:             "/var/lib/sherlock",

	// sherlock's setting memory-path, whose default is relative and resolves
	// against its image's working directory, off this volume
	// (sherlock@8218189:docs/architecture/deployment.md:63,149). It sits one
	// directory down because the outbox is derived beside it
	// (sherlock@8218189:internal/gateway/outbox.go:25-26), so the store and the
	// outbox share one subtree, disjoint from workspaceDirName's.
	memoryPathVariable: "SHERLOCK_MEMORY_PATH",
	memoryFile:         "memory/memory.db",

	// Absolute and this operator's: sherlock's other two roads to a config file
	// are a path relative to a working directory the agent's image declares and a
	// flag this operator does not write, so the directory it resolves against
	// would otherwise be one this operator did not choose
	// (sherlock@fc5fca4:internal/config/config.go:371-395).
	configMountPath: "/run/sherlock/config",

	// Read by os.UserConfigDir on the way to the file rather than by sherlock's
	// settings, which resolve under SHERLOCK_
	// (sherlock@fc5fca4:internal/config/config.go:276-277).
	configHomeVariable: "XDG_CONFIG_HOME",

	// os.UserConfigDir()/sherlock/config.yaml is sherlock's own layout
	// (sherlock@8218189:docs/architecture/deployment.md:84).
	configFile:   "sherlock/config.yaml",
	renderConfig: renderSherlockConfig,

	// Both images default to it (sherlock@9b0e399:internal/config/config.go:34),
	// and it is written to both containers rather than left to them: two
	// defaults agreeing is not the same as one number this operator chose. It is
	// loopback, which is the only bind sherlock's unauthenticated listener
	// accepts (sherlock@8218189:docs/architecture/deployment.md:45-53).
	workspaceAddress: "127.0.0.1:8081",

	// sherlock's settings addr, workspace-addr, workspace and exec-uid, under the
	// SHERLOCK_ prefix and the dash-to-underscore mapping every one of its
	// settings resolves through (sherlock@8218189:docs/architecture/deployment.md:44-47,65,82).
	listenAddressVariable:    "SHERLOCK_ADDR",
	workspaceAddressVariable: "SHERLOCK_WORKSPACE_ADDR",
	workspaceDirVariable:     "SHERLOCK_WORKSPACE",
	// sherlock runs an exec child under the workspace's own account only where
	// this number is the uid that account already has, and refuses every
	// isolated exec otherwise
	// (sherlock@9b0e399:internal/workspace/shell/process_linux.go:131).
	execUserVariable: "SHERLOCK_EXEC_UID",
}

// sherlockConfig is sherlock's config file, holding what this operator has been
// taught to declare and nothing else. The field names are sherlock's setting
// names. A second key family joins it as a second field here: the file is the
// whole of what an agent is configured with, so nothing about its shape is the
// pins'.
type sherlockConfig struct {
	Tools sherlockConfigTools `json:"tools"`
}

type sherlockConfigTools struct {
	Pins map[string]string `json:"pins"`
}

// renderSherlockConfig is the text of the config file an Agent's declaration
// becomes for sherlock.
//
// It is marshalled rather than assembled: a pin is a string this operator does
// not read and cannot constrain, and the one place a foreign string can change
// what a file means is where somebody wrote the file's syntax by hand. Map keys
// marshal in sorted order, so one declaration renders one text and an unchanged
// Agent leaves the workload unchanged.
func renderSherlockConfig(spec agentv1alpha1.AgentSpec) (string, error) {
	file, err := yaml.Marshal(sherlockConfig{Tools: sherlockConfigTools{Pins: spec.Tools.Pins}})
	if err != nil {
		return "", fmt.Errorf("render the config file of the agent: %w", err)
	}

	return string(file), nil
}

// agentTypeClaudeCode is the descriptor for type=claude-code. Admitted by the
// API today but the controller has not yet learned to build it; every caller
// hits ReasonTypeUnimplemented. When the type's implementation lands, this
// descriptor is filled in.
var agentTypeClaudeCode = agentTypeDescriptor{}

// agentTypeCodex is the descriptor for type=codex. Same status as claude-code.
var agentTypeCodex = agentTypeDescriptor{}

// agentTypes is the closed set the API admits. The order does not matter;
// lookups are by exact key.
var agentTypes = map[string]agentTypeDescriptor{
	"sherlock":    agentTypeSherlock,
	"claude-code": agentTypeClaudeCode,
	"codex":       agentTypeCodex,
}

// agentTypeDefault is the type an Agent gets when its spec leaves the field
// unset. It is named for what the controller builds today, not for what is
// privileged; the closed set is the place that decides what is supported, and
// any type in it can be the default with the same one-line change.
const agentTypeDefault = "sherlock"

// resolveAgentType returns the descriptor for the type an Agent's spec names,
// substituting the default when the field is empty. The second return is false
// when the named type is admitted but unimplemented — the caller is then
// expected to refuse the reconcile on the Agent.
func resolveAgentType(specType string) (agentTypeDescriptor, bool) {
	if specType == "" {
		specType = agentTypeDefault
	}

	descriptor, ok := agentTypes[specType]
	if !ok {
		return agentTypeDescriptor{}, false
	}

	return descriptor, descriptor.implemented()
}

// implemented reports whether every field of the descriptor is set. It reads the
// fields by reflection so that a field added to the descriptor is required of
// every type without a second list to keep in step with the struct.
func (d agentTypeDescriptor) implemented() bool {
	for _, field := range reflect.ValueOf(d).Fields() {
		if field.IsZero() {
			return false
		}
	}

	return true
}

// memoryPath is the absolute path of the agent's memory store on its state
// volume.
func (d agentTypeDescriptor) memoryPath() string { return d.stateMountPath + "/" + d.memoryFile }

// configFileIn is where the agent looks for its config file under the
// configuration directory dir.
func (d agentTypeDescriptor) configFileIn(dir string) string { return dir + "/" + d.configFile }
