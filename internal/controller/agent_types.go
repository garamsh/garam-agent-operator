package controller

// agentTypeDescriptor carries everything the controller reads to build a Pod
// for one agent type. The controller looks up the descriptor by Agent.Spec.Type
// once per reconcile and dispatches the rest of the build through it.
//
// One descriptor per supported type. A type admitted by the API but absent
// from the map is refused on reconcile with ReasonTypeUnimplemented; adding a
// new type is a code change, not a configuration one.
type agentTypeDescriptor struct {
	// envPrefix is the prefix every environment variable this controller writes
	// carries, except those whose names are the agent project's own and travel
	// unchanged (XDG_CONFIG_HOME, AGENT_CONFIG_CONTENT). The naming convention
	// matches what viper's SetEnvPrefix reads on the agent's side.
	envPrefix string

	// configDirName is the segment the agent resolves its config file under,
	// the part that hangs off the mount path this operator names. The segment
	// is the agent project's, not this operator's: sherlock resolves
	// os.UserConfigDir/<dir>/config.yaml, claude-code resolves
	// ~/.claude-code/config.yaml, and so on. An agent whose resolution differs
	// is one this descriptor has to be re-examined for.
	configDirName string
}

// agentTypeSherlock is the descriptor for type=sherlock (and the unset default).
// Its env prefix and config dir name follow sherlock's published names on the
// agent project's dev branch.
var agentTypeSherlock = agentTypeDescriptor{
	envPrefix:     "SHERLOCK_",
	configDirName: "sherlock",
}

// agentTypeClaudeCode is the descriptor for type=claude-code. Admitted by the
// API today but the controller has not yet learned to build it; every caller
// hits ReasonTypeUnimplemented. When the type's implementation lands, this
// descriptor is filled in.
var agentTypeClaudeCode = agentTypeDescriptor{}

// agentTypeCodex is the descriptor for type=codex. Same status as claude-code.
var agentTypeCodex = agentTypeDescriptor{}

// agentTypes is the closed set the API admits. The order does not matter;
// lookups are by exact key. A descriptor with zero fields is treated as
// unimplemented and refuses the reconcile, so a half-filled entry is a
// guarantee that the controller will refuse the workload rather than build it
// wrong.
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

	return descriptor, descriptor != agentTypeDescriptor{}
}
