package cli

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/agent/controlapi"
	"github.com/garamsh/garam-agent-operator/internal/gaon/agent/machineapi"
	"github.com/garamsh/garam-agent-operator/internal/gaon/config"
	"github.com/garamsh/garam-agent-operator/internal/gaon/health"
	"github.com/garamsh/garam-agent-operator/internal/gaon/keyfile"
	"github.com/garamsh/garam-agent-operator/internal/gaon/token"
	"github.com/garamsh/garam/lib/errorx"
)

// requestTimeout bounds a call to garam or gaon. The control service's
// long poll is bounded by its own wait.
const requestTimeout = time.Minute

// assemble builds the clients and the reconciler from cfg. Every file
// it reads is read here, so a deployment's mistake stops the binary
// before it does anything. The operator's key and gaon's client key are
// read through keyfile, which refuses one another user can read.
func assemble(cfg config.Config, log *slog.Logger) (*operator, error) {
	garamRoot, garamPool, err := readRoot(cfg.GaramRootFile)
	if err != nil {
		return nil, err
	}
	controlRoot, controlPool, err := readRoot(cfg.ControlRootFile)
	if err != nil {
		return nil, err
	}
	gaonHTTP, err := gaonClient(cfg)
	if err != nil {
		return nil, err
	}
	garamHTTP := &http.Client{Timeout: requestTimeout, Transport: transport(garamPool, tls.VersionTLS12, nil)}
	controlHTTP := &http.Client{Transport: transport(controlPool, tls.VersionTLS13, nil)}

	garamTokens, err := token.New(token.Options{
		MachineURL: cfg.GaramMachineURL, HTTPClient: garamHTTP, ClientID: cfg.OperatorClientID,
		TokenURL: cfg.GaramTokenURL, KeyFile: cfg.OperatorKeyFile,
	})
	if err != nil {
		return nil, err
	}
	controlTokens, err := token.New(token.Options{
		MachineURL: cfg.GaramMachineURL, HTTPClient: garamHTTP, ClientID: cfg.OperatorClientID,
		TokenURL: cfg.GaramTokenURL, KeyFile: cfg.OperatorKeyFile, Audience: cfg.ControlGRN,
	})
	if err != nil {
		return nil, err
	}
	garam, err := machineapi.New(cfg.GaramMachineURL, garamHTTP, garamTokens, cfg.ControlGRN)
	if err != nil {
		return nil, err
	}
	feed, err := controlapi.New(controlapi.Options{
		URL: cfg.ControlURL, HTTP: controlHTTP, Tokens: controlTokens,
		Controller: cfg.OperatorClientID, Wait: controlapi.MaxWait,
	})
	if err != nil {
		return nil, err
	}
	cluster := api.NewClient(cfg.GaonURL, gaonHTTP, api.WithLabelSelector(agent.Selector()))
	// A second client, for the model-key Secrets alone: the first one's
	// selector does not reach them (ADR-0154).
	modelKeys := api.NewClient(cfg.GaonURL, gaonHTTP, api.WithLabelSelector(agent.ModelKeySelector()))
	service := agent.NewService(feed, garam, feed, cluster, agent.Options{
		Image: cfg.AgentImage, GaramRoot: garamRoot, ControlRoot: controlRoot,
		MachineURL: cfg.GuestMachineURL, ControlURL: cfg.GuestControlURL,
		Resources: resources(cfg), ModelKeys: modelKeys, Logger: log,
	})
	return &operator{service: service, settle: feed.Settle, probe: &health.Probe{}, log: log}, nil
}

// resources is the MicroVM's requests and limits, both the configured
// quantities: the Node Agent reads the limits and falls back to the
// requests, and the scheduler reads the requests.
func resources(cfg config.Config) api.ResourceRequirements {
	list := api.ResourceList{CPU: api.Quantity(cfg.AgentCPU), Memory: api.Quantity(cfg.AgentMemory)}
	return api.ResourceRequirements{Requests: list, Limits: list}
}

// readRoot reads a trust root, which is a certificate and not a secret,
// and the pool that trusts it.
func readRoot(path string) ([]byte, *x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errorx.Wrap(err, "read trust root "+path, errorx.KindInvalidArgument)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, nil, errorx.New(errorx.KindInvalidArgument, "trust root "+path+" holds no certificate")
	}
	return pem, pool, nil
}

// gaonClient returns the HTTP client that presents the operator's
// client certificate to gaon and trusts gaon's CA.
func gaonClient(cfg config.Config) (*http.Client, error) {
	_, pool, err := readRoot(cfg.GaonCAFile)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(cfg.GaonClientCertFile)
	if err != nil {
		return nil, errorx.Wrap(err, "read gaon client certificate "+cfg.GaonClientCertFile, errorx.KindInvalidArgument)
	}
	keyPEM, err := keyfile.Read(cfg.GaonClientKeyFile)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errorx.Wrap(err, "gaon client certificate and key", errorx.KindInvalidArgument)
	}
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: transport(pool, tls.VersionTLS12, []tls.Certificate{pair}),
	}, nil
}

func transport(pool *x509.CertPool, minVersion uint16, certs []tls.Certificate) *http.Transport {
	return &http.Transport{
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: minVersion, Certificates: certs},
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
	}
}
