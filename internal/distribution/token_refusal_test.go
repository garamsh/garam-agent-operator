package distribution_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// tokenEndpoint stands in for garam's /oauth/token, answering the n-th exchange the n-th token of
// garam's operator form, and counting the exchanges.
func tokenEndpoint(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	exchanges := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := exchanges.Add(1)
		w.Header().Set("Garam-Contract-Version", "machine-token.v2")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, nthToken(n))
	}))
	t.Cleanup(server.Close)
	return server, exchanges
}

func nthToken(n int32) string { return fmt.Sprintf("garam_ot_%064x", n) }

// managerTransport is the manager's transport to the control service: a token minted for it,
// exchanged at garam under the controller's key, with the controller's GRN beside it.
func managerTransport(t *testing.T, garam, control *httptest.Server) http.RoundTripper {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	files := machinetoken.Files{
		PrivateKey: filepath.Join(dir, "private-key.pem"),
		ClientID:   filepath.Join(dir, "client-id"),
		TokenURL:   filepath.Join(dir, "token-url"),
	}
	for path, content := range map[string][]byte{
		files.PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		files.ClientID:   []byte(controller),
		files.TokenURL:   []byte("https://machine.garam.example/oauth/token"),
	} {
		require.NoError(t, os.WriteFile(path, content, 0o600))
	}
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: garam.URL + "/oauth/token", Files: files, Audience: "grn:root:default:operator:control",
		HTTP: garam.Client(),
	})
	return &machinetoken.Transport{Source: source, Base: control.Client().Transport, ClientIDHeader: "Controller-Grn"}
}

// TestDesired_ATokenGaramRefusesIsExchangedOnceMoreByTheManager is a revoked control-audience token,
// after its key's rotation: garam's 422 is answered 401, and the manager's transport exchanges one
// fresh token and asks once more, rather than meeting 403 until its token's refresh.
func TestDesired_ATokenGaramRefusesIsExchangedOnceMoreByTheManager(t *testing.T) {
	e := newEnv(t)
	garam, exchanges := tokenEndpoint(t)
	e.prover.refused = map[string]bool{nthToken(1): true}
	manager := &http.Client{Transport: managerTransport(t, garam, e.server)}

	got := e.desired(t, manager, "")
	assert.Equal(t, http.StatusOK, got.status, got.message)
	assert.Equal(t, int32(2), exchanges.Load(), "the refused token was not exchanged once more")
	assert.Equal(t, nthToken(1), e.prover.tokens[0])
	assert.Equal(t, nthToken(2), e.prover.tokens[1])

	// Control: a fresh token garam refuses too is answered as it came, after one exchange more.
	e.prover.refused[nthToken(3)], e.prover.refused[nthToken(4)] = true, true
	refused := e.desired(t, &http.Client{Transport: managerTransport(t, garam, e.server)}, "")
	assert.Equal(t, http.StatusUnauthorized, refused.status, refused.message)
	assert.Equal(t, int32(4), exchanges.Load(), "a refused fresh token was exchanged again")
}
