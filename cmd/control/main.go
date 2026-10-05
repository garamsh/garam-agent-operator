// Command control runs the control service: it opens its PostgreSQL store, applies the
// store's schema, serves the console's routes under garam's operation authority, and serves
// its health until it is stopped.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/garamsh/garam-agent-operator/internal/certificate"
	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/introspector"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/issuer"
	"github.com/garamsh/garam-agent-operator/internal/definition/registrar"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
	"github.com/garamsh/garam-agent-operator/internal/distribution/prover"
	"github.com/garamsh/garam-agent-operator/internal/execution"
	executiongaram "github.com/garamsh/garam-agent-operator/internal/execution/garam"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// databaseURLVariable names the environment variable holding the store's connection URL,
// which carries a password and so is not a flag.
const databaseURLVariable = "CONTROL_DATABASE_URL"

// shutdownTimeout bounds how long each server drains on stop.
const shutdownTimeout = 5 * time.Second

// garamTimeout bounds one call to garam's machine listener.
const garamTimeout = 10 * time.Second

// feedPollInterval is how often a waiting request for the desired feed reads the position again.
const feedPollInterval = time.Second

// feedMaxAgents bounds the candidate agents one answer of the desired feed carries, and so the
// agent proofs one answer asks garam for.
const feedMaxAgents = 500

// apiWriteTimeout bounds writing one answer, above the desired feed's longest wait.
const apiWriteTimeout = 45 * time.Second

type options struct {
	probeAddr       string
	apiAddr         string
	apiCertificate  string
	apiKey          string
	garamURL        string
	serverRootFile  string
	certificateFile string
	keyFile         string
}

func main() {
	var o options
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the health endpoints bind to.")
	flag.StringVar(&o.apiAddr, "api-bind-address", ":8080", "The address the console's routes bind to, over TLS.")
	flag.StringVar(&o.apiCertificate, "api-certificate-file", "",
		"PEM file holding the certificate chain the console's routes are served under.")
	flag.StringVar(&o.apiKey, "api-key-file", "", "PEM file holding that certificate's private key.")
	flag.StringVar(&o.garamURL, "garam-machine-url", "", "The base URL of garam's machine listener.")
	flag.StringVar(&o.serverRootFile, "garam-server-root-file", "",
		"PEM file holding the garam server root the machine listener's certificate chains to.")
	flag.StringVar(&o.certificateFile, "operator-certificate-file", "",
		"PEM file holding this service's operator certificate, whose SAN URI is its operator GRN.")
	flag.StringVar(&o.keyFile, "operator-key-file", "", "PEM file holding the operator certificate's private key.")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, o, os.Getenv(databaseURLVariable)); err != nil {
		slog.Error("control service stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options, databaseURL string) error {
	if databaseURL == "" {
		return fmt.Errorf("%s is not set", databaseURLVariable)
	}
	apiTLS, err := apiTLSConfig(o)
	if err != nil {
		return err
	}
	machine, audience, err := garamClient(o)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer pool.Close()

	store := repository.NewPostgres(pool)
	if err := store.ApplySchema(ctx); err != nil {
		return err
	}
	slog.Info("store schema applied")

	garam := garammachine.New(o.garamURL, machine)
	definitions := definition.NewService(store, registrar.NewGaram(garam), issuer.NewGaram(garam))
	api := http.NewServeMux()
	api.Handle("/v1/orgs/", console.NewHandler(console.Config{
		Definitions:  definitions,
		Introspector: introspector.NewGaram(garam),
		Audience:     audience,
		Now:          time.Now,
		Logger:       slog.Default(),
	}))
	api.Handle("/v1/operators/", distribution.NewHandler(distribution.Config{
		Definitions:  definitions,
		Prover:       prover.NewGaram(garam),
		PollInterval: feedPollInterval,
		MaxAgents:    feedMaxAgents,
		Logger:       slog.Default(),
	}))
	api.Handle("/v1/agents/", execution.NewHandler(execution.Config{
		Definitions: definitions,
		Garam:       executiongaram.NewGaram(garam),
		Logger:      slog.Default(),
	}))

	health := http.NewServeMux()
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	health.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "store unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	healthServer := &http.Server{Addr: o.probeAddr, Handler: health, ReadHeaderTimeout: 5 * time.Second}
	// Every console request carries a bearer operation authority, so its routes are served over TLS only.
	apiServer := &http.Server{
		Addr: o.apiAddr, Handler: api, TLSConfig: apiTLS,
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: apiWriteTimeout,
	}
	servers := []*http.Server{healthServer, apiServer}
	served := make(chan error, len(servers))
	go func() { served <- fmt.Errorf("serve %s: %w", healthServer.Addr, healthServer.ListenAndServe()) }()
	go func() { served <- fmt.Errorf("serve %s: %w", apiServer.Addr, apiServer.ListenAndServeTLS("", "")) }()
	slog.Info("serving", "health", o.probeAddr, "api", o.apiAddr, "audience", audience)

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("stop %s: %w", server.Addr, err)
		}
	}
	return nil
}

// apiTLSConfig serves the API's certificate, read again whenever its files change, and refuses
// to go on without one. A client certificate is requested and not verified here: a controller
// route has garam prove the leaf a controller presents, and a console route reads none.
func apiTLSConfig(o options) (*tls.Config, error) {
	if o.apiCertificate == "" || o.apiKey == "" {
		return nil, errors.New("--api-certificate-file and --api-key-file are both required")
	}
	reloader, err := certificate.NewReloader(o.apiCertificate, o.apiKey)
	if err != nil {
		return nil, fmt.Errorf("load api certificate: %w", err)
	}
	return &tls.Config{
		GetCertificate: reloader.GetCertificate,
		ClientAuth:     tls.RequestClientCert,
		MinVersion:     tls.VersionTLS13,
	}, nil
}

// garamClient builds the mutual-TLS client garam's machine listener requires, and reads this
// service's operator GRN, the audience of every authority it accepts, from the certificate.
func garamClient(o options) (*http.Client, string, error) {
	if o.garamURL == "" || o.serverRootFile == "" || o.certificateFile == "" || o.keyFile == "" {
		return nil, "", errors.New("--garam-machine-url, --garam-server-root-file, " +
			"--operator-certificate-file and --operator-key-file are all required")
	}
	pair, err := tls.LoadX509KeyPair(o.certificateFile, o.keyFile)
	if err != nil {
		return nil, "", fmt.Errorf("load operator certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, "", fmt.Errorf("parse operator certificate: %w", err)
	}
	if len(leaf.URIs) != 1 {
		return nil, "", fmt.Errorf("operator certificate carries %d SAN URIs, want its GRN alone", len(leaf.URIs))
	}
	root, err := os.ReadFile(o.serverRootFile)
	if err != nil {
		return nil, "", fmt.Errorf("read garam server root: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return nil, "", errors.New("garam server root file holds no certificate")
	}
	client := &http.Client{
		Timeout: garamTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{pair},
			RootCAs:      roots,
			MinVersion:   tls.VersionTLS13,
		}},
	}
	return client, leaf.URIs[0].String(), nil
}
