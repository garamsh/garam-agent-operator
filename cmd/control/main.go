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

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/introspector"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

// databaseURLVariable names the environment variable holding the store's connection URL,
// which carries a password and so is not a flag.
const databaseURLVariable = "CONTROL_DATABASE_URL"

// shutdownTimeout bounds how long each server drains on stop.
const shutdownTimeout = 5 * time.Second

// garamTimeout bounds one call to garam's machine listener.
const garamTimeout = 10 * time.Second

type options struct {
	probeAddr       string
	apiAddr         string
	garamURL        string
	serverRootFile  string
	certificateFile string
	keyFile         string
}

func main() {
	var o options
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the health endpoints bind to.")
	flag.StringVar(&o.apiAddr, "api-bind-address", ":8080", "The address the console's routes bind to.")
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

	// No route creates an agent yet, so no Registrar is wired (issue #211).
	definitions := definition.NewService(store, nil)
	api := console.NewHandler(console.Config{
		Definitions:  definitions,
		Introspector: introspector.NewGaram(o.garamURL, machine),
		Audience:     audience,
		Now:          time.Now,
		Logger:       slog.Default(),
	})

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

	servers := []*http.Server{
		{Addr: o.probeAddr, Handler: health, ReadHeaderTimeout: 5 * time.Second},
		{Addr: o.apiAddr, Handler: api, ReadHeaderTimeout: 5 * time.Second},
	}
	served := make(chan error, len(servers))
	for _, server := range servers {
		go func() { served <- fmt.Errorf("serve %s: %w", server.Addr, server.ListenAndServe()) }()
	}
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
