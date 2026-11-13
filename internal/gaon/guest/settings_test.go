package guest_test

import (
	"maps"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

var agentGRN = grn.New("org1", "agent", "144460a71649d1a1").String()

func environment(overrides map[string]string) func(string) string {
	env := map[string]string{
		"GUEST_AGENT_GRN":        agentGRN,
		"GUEST_ASSIGNMENT_EPOCH": "7",
		"GUEST_MACHINE_URL":      "https://garam.example",
		"GUEST_CONTROL_URL":      "https://control.example",
		"PATH":                   "/usr/bin",
		"SSL_CERT_FILE":          "/etc/ssl/certs/ca-certificates.crt",
		"MODEL_API_KEY":          "must-not-reach-a-program",
	}
	maps.Copy(env, overrides)
	return func(k string) string { return env[k] }
}

func TestFromEnv_NamesTheThreeProgramsInStartOrder(t *testing.T) {
	cfg, err := guest.FromEnv(environment(nil), nil, nil, nil)
	require.NoError(t, err)

	require.Len(t, cfg.Programs, 3)
	names := []string{cfg.Programs[0].Name, cfg.Programs[1].Name, cfg.Programs[2].Name}
	assert.Equal(t, []string{"runtime", "workspace", "adapter"}, names)
	assert.Equal(t, ":8088", cfg.ReadyAddr)
	assert.Contains(t, cfg.Programs[0].Args, agentGRN)
	assert.Contains(t, cfg.Programs[0].Args, "7")
	assert.Contains(t, cfg.Programs[1].Env, "SHERLOCK_EXEC_UID=10001")
	assert.Contains(t, cfg.Programs[2].Env, "GARAM_ADAPTER_AGENT="+agentGRN)
	assert.Contains(t, cfg.Programs[2].Env, "GARAM_ADAPTER_TOKEN_FILE=/run/garam/agent-token")
}

func TestFromEnv_GivesAProgramOnlyWhatItNames(t *testing.T) {
	cfg, err := guest.FromEnv(environment(nil), nil, nil, nil)
	require.NoError(t, err)

	for _, p := range cfg.Programs {
		assert.Contains(t, p.Env, "PATH=/usr/bin", p.Name)
		for _, entry := range p.Env {
			assert.NotContains(t, entry, "MODEL_API_KEY", p.Name)
			assert.NotContains(t, entry, "GUEST_", p.Name)
		}
	}
}

func TestFromEnv_GivesTheModelCredentialToTheRuntimeAlone(t *testing.T) {
	cfg, err := guest.FromEnv(environment(map[string]string{"GUEST_RUNTIME_ENV": "MODEL_API_KEY, UNSET_NAME"}), nil, nil, nil)
	require.NoError(t, err)

	assert.Contains(t, cfg.Programs[0].Env, "MODEL_API_KEY=must-not-reach-a-program")
	for _, p := range cfg.Programs[1:] {
		for _, entry := range p.Env {
			assert.NotContains(t, entry, "MODEL_API_KEY", p.Name)
		}
	}
}

func TestFromEnv_NamesTheConfigurationAndEgoFilesToTheRuntimeOnly(t *testing.T) {
	cfg, err := guest.FromEnv(environment(map[string]string{
		"GUEST_RUNTIME_CONFIG": guest.RuntimeConfigFile, "GUEST_RUNTIME_EGO": guest.RuntimeEgoFile,
	}), nil, nil, nil)
	require.NoError(t, err)

	args := cfg.Programs[0].Args
	assert.Contains(t, strings.Join(args, " "), "--config "+guest.RuntimeConfigFile)
	assert.Contains(t, strings.Join(args, " "), "--ego-file "+guest.RuntimeEgoFile)
	for _, p := range cfg.Programs[1:] {
		assert.NotContains(t, strings.Join(p.Args, " "), "--ego-file", p.Name)
		assert.NotContains(t, strings.Join(p.Args, " "), "--config", p.Name)
	}

	bare, err := guest.FromEnv(environment(nil), nil, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, bare.Programs[0].Args, "--ego-file", "no ego, no flag")
	assert.NotContains(t, bare.Programs[0].Args, "--config", "no configuration, no flag")
}

func TestFromEnv_DropsTheRuntimeAndTheAdapterToTheirAccount(t *testing.T) {
	cfg, err := guest.FromEnv(environment(nil), nil, nil, nil)
	require.NoError(t, err)

	want := &syscall.Credential{Uid: guest.UID, Gid: guest.UID, Groups: []uint32{}}
	assert.Equal(t, want, cfg.Programs[0].Cred)
	assert.Nil(t, cfg.Programs[1].Cred, "the workspace stays root")
	assert.Equal(t, want, cfg.Programs[2].Cred)

	workspace := cfg.Programs[1]
	assert.Equal(t, "/usr/bin/setpriv", workspace.Path)
	assert.Equal(t, []string{"--bounding-set", "-all,+setuid,+setgid,+chown,+sys_admin,+kill", "/sherlock", "workspace"}, workspace.Args)
}

func TestFromEnv_MakesTheStoreAndTheOutboxPrivate(t *testing.T) {
	cfg, err := guest.FromEnv(environment(nil), nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, []guest.Dir{
		{Path: "/var/lib/garam/memory", Mode: 0o700},
		{Path: "/var/lib/garam/memory/outbox", Mode: 0o700},
	}, cfg.Prepare)
}

func TestFromEnv_RefusesWhatItCannotRunAGenerationFrom(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
	}{
		{"an agent that is not a GRN", map[string]string{"GUEST_AGENT_GRN": "agent"}},
		{"an epoch with a leading zero", map[string]string{"GUEST_ASSIGNMENT_EPOCH": "07"}},
		{"a missing epoch", map[string]string{"GUEST_ASSIGNMENT_EPOCH": ""}},
		{"a missing machine URL", map[string]string{"GUEST_MACHINE_URL": ""}},
		{"a missing control URL", map[string]string{"GUEST_CONTROL_URL": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := guest.FromEnv(environment(tt.overrides), nil, nil, nil)
			require.Error(t, err)
			assert.True(t, errorx.IsKind(err, errorx.KindInvalidArgument))
		})
	}
}
