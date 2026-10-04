//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startTimeout bounds how long a binary started by a test has to exit or become ready.
const startTimeout = 30 * time.Second

// withoutFlag is the binary's arguments with flag and its value left out, on fresh ports.
func withoutFlag(t *testing.T, flag string) (args []string, probeAddr string) {
	t.Helper()
	args = slices.Clone(controlArgs)
	for _, port := range []string{"--health-probe-bind-address", "--api-bind-address"} {
		addr, err := freeAddress()
		require.NoError(t, err)
		args[slices.Index(args, port)+1] = addr
	}
	probeAddr = args[slices.Index(args, "--health-probe-bind-address")+1]
	if i := slices.Index(args, flag); i >= 0 {
		args = slices.Delete(args, i, i+2)
	}
	return args, probeAddr
}

func TestBinary_RefusesToStartWithoutItsAPICertificate(t *testing.T) {
	for _, flag := range []string{"--api-certificate-file", "--api-key-file"} {
		t.Run(flag, func(t *testing.T) {
			args, _ := withoutFlag(t, flag)
			ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, binaryPath, args...)
			cmd.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			err := cmd.Run()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "the binary started without %s", flag)
			assert.Contains(t, stderr.String(), "--api-certificate-file and --api-key-file are both required")
		})
	}

	// Control: with both files the same arguments start a binary that becomes ready.
	args, probeAddr := withoutFlag(t, "")
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	require.NoError(t, waitReady("http://"+probeAddr+"/readyz"))
}

func TestBinary_ServesTheConsoleOnlyOverTLS(t *testing.T) {
	plain := strings.Replace(revisionsURL(), "https://", "http://", 1)
	req, err := http.NewRequest(http.MethodPost, plain, bytes.NewReader([]byte(`{"requestId":"c1"}`)))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation an-authority")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	// The TLS listener answers a plaintext request itself; it never reaches the route.
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Type"), "a plaintext request reached the route's JSON answer")

	// Control: the same route over TLS reaches the route, which answers its own refusal.
	tlsResp := postConfigure(t, "")
	assert.Equal(t, http.StatusUnauthorized, tlsResp.StatusCode)
	assert.Equal(t, "application/json", tlsResp.Header.Get("Content-Type"))
}
