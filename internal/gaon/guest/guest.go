// Package guest is the supervisor that runs inside a gaon MicroVM: it
// starts the agent runtime, the workspace and garam's adapter in that
// order and drains them in the same order (ADR-0153). It runs as root:
// the runtime and the adapter drop to UID, and the workspace keeps the
// capabilities it needs and hands its executor ExecUID.
package guest

import (
	"io"
	"log/slog"
	"os"
	"syscall"
)

// What the operator renders and the supervisor reads. The operator's
// MicroVM names these paths and this port, so both sides share them.
const (
	// UID is the account of the runtime and the adapter, and of every
	// file the guest keeps from them.
	UID = 65532
	// ExecUID is the account the workspace runs an Exec's child as.
	ExecUID = 10001
	// CredentialsDir holds every Secret-sourced file, mode 0400.
	CredentialsDir = "/run/garam/"
	// MemoryDir and WorkspaceDir are where the two Volumes mount.
	MemoryDir    = "/var/lib/garam/memory"
	OutboxDir    = MemoryDir + "/outbox"
	WorkspaceDir = "/workspace"
	// ReadyPort is the supervisor's readiness listener.
	ReadyPort = 8088
	// BinaryPath is where the image holds the supervisor, which the
	// MicroVM's command names because a command replaces the image's
	// entrypoint.
	BinaryPath = "/usr/local/bin/gaon-guest"
)

// The files below CredentialsDir, named as the operator's Secret keys.
// ControlTokenFile is the token minted with the control service as
// audience; PlacementTokenFile is the token the operator mints into the
// agent's Secret.
const (
	GaramRootFile      = "garam-root.pem"
	ControlRootFile    = "control-root.pem"
	AgentTokenFile     = "agent-token"
	PlacementTokenFile = "placement-token"
	ControlTokenFile   = "control-token"
)

// The files the operator writes into the MicroVM for the runtime's own
// configuration, beside the credentials: owned by UID and readable by
// it alone, which is what keeps a tool the workspace runs (ExecUID)
// from reading the pins the file holds.
const (
	RuntimeConfigFile = CredentialsDir + "runtime-config.yaml"
	RuntimeEgoFile    = CredentialsDir + "runtime-ego.md"
)

// Program is one process of a generation. Its environment is exactly
// Env: nothing is inherited from the supervisor. Cred, when set, is the
// account it runs as, with no supplementary group; nil keeps the
// supervisor's.
type Program struct {
	Name string
	Path string
	Args []string
	Env  []string
	Cred *syscall.Credential
}

// Dir is a directory the supervisor gives to UID, at Mode, before it
// starts any program.
type Dir struct {
	Path string
	Mode os.FileMode
}

// Config is one generation: the directories to make private, the
// programs in start order, where the readiness listener opens once all
// of them run, and where their output and the supervisor's log go.
type Config struct {
	Prepare   []Dir
	Programs  []Program
	ReadyAddr string
	Stdout    io.Writer
	Stderr    io.Writer
	Log       *slog.Logger
}
