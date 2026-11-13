package guest

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"syscall"

	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// Where the image holds each program, and the loopback addresses the
// runtime and the workspace listen on (the standalone runner's).
const (
	sherlockPath  = "/sherlock"
	garamPath     = "/usr/local/bin/garam"
	setprivPath   = "/usr/bin/setpriv"
	gatewayAddr   = "127.0.0.1:8080"
	workspaceAddr = "127.0.0.1:8081"
)

// workspaceCaps are the capabilities the workspace keeps, the
// standalone runner's five: it sets its executor's user and group, owns
// the workspace, enters the runtime's network namespace and kills what
// an Exec started.
const workspaceCaps = "-all,+setuid,+setgid,+chown,+sys_admin,+kill"

// The settings the operator renders into the guest's environment. They
// are addresses, identifiers, two file paths and the names of variables
// for the runtime: a credential for garam is a file below
// CredentialsDir, and the supervisor's value for a runtime variable
// reaches the runtime alone.
const (
	EnvAgent         = "GUEST_AGENT_GRN"
	EnvEpoch         = "GUEST_ASSIGNMENT_EPOCH"
	EnvMachineURL    = "GUEST_MACHINE_URL"
	EnvControlURL    = "GUEST_CONTROL_URL"
	EnvRuntimeConfig = "GUEST_RUNTIME_CONFIG"
	EnvRuntimeEgo    = "GUEST_RUNTIME_EGO"
	EnvRuntimeEnv    = "GUEST_RUNTIME_ENV"
)

// inherited are the variables a program takes from the supervisor's
// environment, as the images set them.
var inherited = []string{"PATH", "SSL_CERT_FILE", "SHERLOCK_TOOLS_DIR"}

// FromEnv reads one generation's settings and returns it with the three
// programs in start order: runtime, workspace, adapter.
func FromEnv(getenv func(string) string, stdout, stderr io.Writer, log *slog.Logger) (Config, error) {
	agent, err := parseAgent(getenv(EnvAgent))
	if err != nil {
		return Config{}, err
	}
	epoch := getenv(EnvEpoch)
	if n, err := strconv.ParseUint(epoch, 10, 63); err != nil || strconv.FormatUint(n, 10) != epoch {
		return Config{}, errorx.New(errorx.KindInvalidArgument, EnvEpoch+" is not a canonical decimal string")
	}
	machineURL, controlURL := getenv(EnvMachineURL), getenv(EnvControlURL)
	if machineURL == "" {
		return Config{}, errorx.New(errorx.KindInvalidArgument, EnvMachineURL+" is required")
	}
	if controlURL == "" {
		return Config{}, errorx.New(errorx.KindInvalidArgument, EnvControlURL+" is required")
	}

	var base, runtimeOnly []string
	for _, k := range inherited {
		if v := getenv(k); v != "" {
			base = append(base, k+"="+v)
		}
	}
	for _, name := range runtimeNames(getenv(EnvRuntimeEnv)) {
		if v := getenv(name); v != "" {
			runtimeOnly = append(runtimeOnly, name+"="+v)
		}
	}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	file := func(name string) string { return CredentialsDir + name }

	runtime := []string{
		"agent", "--agent-id", agent, "--assignment-epoch", epoch,
		"--memory-path", MemoryDir + "/memory.db",
		"--addr", gatewayAddr, "--workspace-addr", workspaceAddr,
	}
	if config := getenv(EnvRuntimeConfig); config != "" {
		runtime = append(runtime, "--config", config)
	}
	if ego := getenv(EnvRuntimeEgo); ego != "" {
		runtime = append(runtime, "--ego-file", ego)
	}
	// The runtime and the adapter run as UID with no capability and no
	// supplementary group; the workspace stays root with workspaceCaps.
	account := func() *syscall.Credential {
		return &syscall.Credential{Uid: UID, Gid: UID, Groups: []uint32{}}
	}
	return Config{
		Prepare: []Dir{{Path: MemoryDir, Mode: 0o700}, {Path: OutboxDir, Mode: 0o700}},
		Programs: []Program{
			{Name: "runtime", Path: sherlockPath, Args: runtime, Cred: account(), Env: with(runtimeOnly...)},
			{Name: "workspace", Path: setprivPath, Args: []string{"--bounding-set", workspaceCaps, sherlockPath, "workspace"}, Env: with(
				"SHERLOCK_WORKSPACE="+WorkspaceDir, fmt.Sprintf("SHERLOCK_EXEC_UID=%d", ExecUID))},
			{Name: "adapter", Path: garamPath, Args: []string{"adapter"}, Cred: account(), Env: with(
				"GARAM_ADAPTER_AGENT="+agent,
				"GARAM_ADAPTER_MACHINE_URL="+machineURL,
				"GARAM_ADAPTER_SERVER_ROOT_FILE="+file(GaramRootFile),
				"GARAM_ADAPTER_CONTROL_URL="+controlURL,
				"GARAM_ADAPTER_CONTROL_ROOT_FILE="+file(ControlRootFile),
				"GARAM_ADAPTER_PLACEMENT_TOKEN_FILE="+file(PlacementTokenFile),
				"GARAM_ADAPTER_OUTBOX_DIR="+OutboxDir,
				"GARAM_ADAPTER_TOKEN_FILE="+file(AgentTokenFile),
				"GARAM_ADAPTER_CONTROL_TOKEN_FILE="+file(ControlTokenFile))},
		},
		ReadyAddr: fmt.Sprintf(":%d", ReadyPort),
		Stdout:    stdout,
		Stderr:    stderr,
		Log:       log,
	}, nil
}

// parseAgent returns s when it is a canonical agent GRN.
func parseAgent(s string) (string, error) {
	g, err := grn.Parse(s)
	if err != nil || g.ServiceType != "agent" || g.Locator != "" || g.String() != s {
		return "", errorx.New(errorx.KindInvalidArgument, EnvAgent+" is not a canonical agent GRN")
	}
	return s, nil
}

// runtimeNames splits the comma-separated variable names list.
func runtimeNames(list string) []string {
	var names []string
	for n := range strings.SplitSeq(list, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}
