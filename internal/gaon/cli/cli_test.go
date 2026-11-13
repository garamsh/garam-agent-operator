package cli_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/cli"
	"github.com/garamsh/garam-agent-operator/tests/gaon/gaonfake"
	"github.com/garamsh/garam-agent-operator/tests/gaon/garamfake"
	"github.com/garamsh/garam/lib/grn"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRun_RefusesWithoutSettingsAndNamesThem(t *testing.T) {
	var out strings.Builder

	err := cli.Run(t.Context(), env(nil), &out)

	require.Error(t, err)
	assert.Contains(t, out.String(), "GAON_API_URL")
	assert.Contains(t, out.String(), "GARAM_TOKEN_URL")
	assert.Contains(t, out.String(), "CONTROL_GRN")
}

func TestRun_RefusesASettingGaonWouldRefuseAndNamesIt(t *testing.T) {
	s := newSetup(t)
	s.env["AGENT_IMAGE"] = "registry.example/agent:1"
	var out strings.Builder

	err := cli.Run(t.Context(), env(s.env), &out)

	require.Error(t, err)
	assert.Contains(t, out.String(), "AGENT_IMAGE")
	assert.Equal(t, 0, s.gaon.Count("PUT", "microvms"))
}

// pki writes a self-signed certificate and its key, the key as 0600.
func pki(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certPath, keyPath
}

// feed is a stand-in control service. It answers the first ask at once
// and holds every later one, as the long poll does.
type feed struct {
	mu   sync.Mutex
	seen []http.Header
}

func (f *feed) handler(agentID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Clone())
		f.mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Query().Get("after") != "" {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"cursor":"1","agents":[{"agent":"`+agentID+`","revision":"1","epoch":"1",`+
			`"profile":{"name":"std","version":1,"resources":{},"storageSize":"1Gi"},"configuration":{}}]}`)
	})
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

type setup struct {
	env   map[string]string
	garam *garamfake.Server
	gaon  *gaonfake.Server
	feed  *feed
	agent grn.GRN
}

func newSetup(t *testing.T) setup {
	t.Helper()
	dir := t.TempDir()
	cert, key := pki(t, dir)
	opKey, opPriv := garamfake.WriteKey(t)
	operator := grn.New("org1", "operator", "mgr1").String()
	g := garamfake.New(t, operator, &opPriv.PublicKey)
	gaon, _ := gaonfake.New(t, agent.Selector())

	a := grn.New("org1", "agent", "a1")
	f := &feed{}
	ctl := httptest.NewServer(f.handler(a.String()))
	t.Cleanup(ctl.Close)

	return setup{
		garam: g, gaon: gaon, feed: f, agent: a,
		env: map[string]string{
			"GAON_API_URL": gaon.URL(), "GAON_CA_FILE": cert, "GAON_CLIENT_CERT_FILE": cert, "GAON_CLIENT_KEY_FILE": key,
			"GARAM_MACHINE_URL": g.URL(), "GARAM_ROOT_FILE": cert, "GARAM_TOKEN_URL": g.TokenURL(),
			"CONTROL_URL": ctl.URL, "CONTROL_ROOT_FILE": cert, "CONTROL_GRN": grn.New("org1", "operator", "ctl1").String(),
			"OPERATOR_KEY_FILE": opKey, "OPERATOR_CLIENT_ID": operator,
			"AGENT_IMAGE": "registry.example/agent@sha256:" + strings.Repeat("0", 64),
			"AGENT_CPU":   "1", "AGENT_MEMORY": "1Gi", "HEALTH_ADDR": freeAddr(t),
		},
	}
}

func TestRun_ExchangesReadsTheFeedRendersAndStopsGracefully(t *testing.T) {
	s := newSetup(t)
	runCtx, stop := context.WithCancel(t.Context())
	var out strings.Builder
	done := make(chan error, 1)
	go func() { done <- cli.Run(runCtx, env(s.env), &out) }()

	require.Eventually(t, func() bool { return s.gaon.Count("PUT", "microvms") == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		res, err := http.Get("http://" + s.env["HEALTH_ADDR"] + "/readyz")
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 10*time.Second, 20*time.Millisecond)

	// The MicroVM names the configured resources, which gaon's Node
	// Agent needs before it starts one.
	for _, r := range s.gaon.Requests() {
		if r.Method == "PUT" && r.Kind == "microvms" {
			m, ok := s.gaon.MicroVM(t, r.Name)
			require.True(t, ok)
			assert.Equal(t, api.ResourceList{CPU: "1", Memory: "1Gi"}, m.Spec.Resources.Limits)
			assert.Equal(t, api.ResourceList{CPU: "1", Memory: "1Gi"}, m.Spec.Resources.Requests)
		}
	}

	// Two exchanges: garam's own and the control service's.
	exchanges := s.garam.Exchanges()
	audiences := make([]string, 0, len(exchanges))
	for _, e := range exchanges {
		audiences = append(audiences, e.Audience)
	}
	assert.ElementsMatch(t, []string{"", s.env["CONTROL_GRN"]}, audiences)
	assert.Len(t, s.garam.Mints(), 2)
	s.feed.mu.Lock()
	assert.Equal(t, s.env["OPERATOR_CLIENT_ID"], s.feed.seen[0].Get("Controller-Grn"))
	s.feed.mu.Unlock()

	stop()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
	}
	assert.NotContains(t, out.String(), "garam_ot_")
	assert.NotContains(t, out.String(), "garam_at_")
}

func TestRun_RefusesAKeyAnotherUserCanRead(t *testing.T) {
	s := newSetup(t)
	require.NoError(t, os.Chmod(s.env["OPERATOR_KEY_FILE"], 0o644))
	var out strings.Builder

	err := cli.Run(t.Context(), env(s.env), &out)

	require.Error(t, err)
	assert.Contains(t, out.String(), "readable beyond its owner")
	assert.Zero(t, s.gaon.Count("PUT", "microvms"))

	// Control: the same settings with the key at 0600 start.
	require.NoError(t, os.Chmod(s.env["OPERATOR_KEY_FILE"], 0o600))
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- cli.Run(ctx, env(s.env), io.Discard) }()
	require.Eventually(t, func() bool { return s.gaon.Count("PUT", "microvms") == 1 }, 10*time.Second, 20*time.Millisecond)
	stop()
	assert.NoError(t, <-done)
}

func TestRun_RefusesAGaonKeyAnotherUserCanRead(t *testing.T) {
	s := newSetup(t)
	require.NoError(t, os.Chmod(s.env["GAON_CLIENT_KEY_FILE"], 0o640))
	var out strings.Builder

	require.Error(t, cli.Run(t.Context(), env(s.env), &out))
	assert.Contains(t, out.String(), "readable beyond its owner")
}
