// Command control runs the control service: it opens its PostgreSQL store, applies the
// store's schema, serves the console's routes under garam's operation authority, and serves
// its health until it is stopped. Its publish-profile subcommand publishes a deployment's
// profile versions into that store, and serves nothing.
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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/garamsh/garam-agent-operator/internal/certificate"
	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/introspector"
	"github.com/garamsh/garam-agent-operator/internal/console/lifecycle"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/registrar"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
	"github.com/garamsh/garam-agent-operator/internal/distribution/prover"
	"github.com/garamsh/garam-agent-operator/internal/execution"
	executiongaram "github.com/garamsh/garam-agent-operator/internal/execution/garam"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
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

// garamTokenPath is garam's exchangeClientAssertion route under the machine listener's base URL.
const garamTokenPath = "/oauth/token"

// garamIdentityPath is garam's getIdentity route under the same base URL, which the token source
// calls to observe its token's acceptance (ADR 0075).
const garamIdentityPath = "/identity"

type options struct {
	probeAddr      string
	apiAddr        string
	apiCertificate string
	apiKey         string
	garamURL       string
	serverRootFile string
	operatorKeys   machinetoken.Files
	consoleOrigins []string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == publishProfileCommand {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := publishProfiles(ctx, os.Args[2:], os.Getenv(databaseURLVariable), os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	var o options
	flag.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the health endpoints bind to.")
	flag.StringVar(&o.apiAddr, "api-bind-address", ":8080", "The address the console's routes bind to, over TLS.")
	flag.StringVar(&o.apiCertificate, "api-certificate-file", "",
		"PEM file holding the certificate chain the console's routes are served under.")
	flag.StringVar(&o.apiKey, "api-key-file", "", "PEM file holding that certificate's private key.")
	flag.StringVar(&o.garamURL, "garam-machine-url", "", "The base URL of garam's machine listener.")
	flag.StringVar(&o.serverRootFile, "garam-server-root-file", "",
		"PEM file holding the garam server root the machine listener's certificate chains to.")
	flag.StringVar(&o.operatorKeys.PrivateKey, "operator-private-key-file", "",
		"PKCS#8 PEM file holding the private key garam issued this service's operator, read at every exchange.")
	flag.StringVar(&o.operatorKeys.ClientID, "operator-client-id-file", "",
		"File holding this service's operator GRN, the clientId garam answered beside its key, and the audience "+
			"every authority this service accepts must name.")
	flag.StringVar(&o.operatorKeys.TokenURL, "operator-token-url-file", "",
		"File holding the tokenUrl garam answered beside this service's key, which every assertion names as "+
			"its audience.")
	flag.Func("console-origin",
		"A browser origin the console calls from, exactly as a browser sends it: scheme, host and any port, "+
			"with no path. Repeat for each. Only these get a CORS answer; none set answers no CORS at all.",
		func(value string) error {
			origin, err := consoleOrigin(value)
			if err != nil {
				return err
			}
			o.consoleOrigins = append(o.consoleOrigins, origin)
			return nil
		})
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
	machine, source, audience, err := garamClient(o)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer pool.Close()

	store := repository.NewPostgres(pool)
	if err := store.Migrate(ctx, slog.Default()); err != nil {
		return err
	}

	garam := garammachine.New(o.garamURL, machine)
	definitions := definition.NewService(store, registrar.NewGaram(garam))
	api := http.NewServeMux()
	api.Handle("/v1/orgs/", console.NewHandler(console.Config{
		Definitions:  definitions,
		Introspector: introspector.NewGaram(garam),
		Lifecycle:    lifecycle.NewGaram(garam),
		Audience:     audience,
		Now:          time.Now,
		Logger:       slog.Default(),

		ConsoleOrigins: o.consoleOrigins,
	}))
	api.Handle("/v1/operators/", distribution.NewHandler(distribution.Config{
		Definitions:  definitions,
		Prover:       prover.NewGaram(garam),
		PollInterval: feedPollInterval,
		MaxAgents:    feedMaxAgents,
		Logger:       slog.Default(),
		Now:          time.Now,
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
		// Every route proves its caller at garam, so a lost authentication fails them all (ADR 0075).
		if err := source.Check(); err != nil {
			http.Error(w, "garam authentication lost", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// Kept exchanging while no request calls garam, so a recovery clears /readyz with no traffic,
	// which a service out of its Service's endpoints receives none of.
	go source.Keep(ctx)

	healthServer := &http.Server{Addr: o.probeAddr, Handler: health, ReadHeaderTimeout: 5 * time.Second}
	// Every console request carries a bearer operation authority, so its routes are served over TLS only.
	apiServer := &http.Server{
		Addr: o.apiAddr, Handler: noRedirects(api), TLSConfig: apiTLS,
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
// to go on without one. No client certificate is requested: a controller route has garam prove the
// token a controller presents, and a console route the authority it carries.
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
		MinVersion:     tls.VersionTLS13,
	}, nil
}

// garamClient builds the client garam's machine listener is called through: it verifies the
// listener against the garam server root and presents this service's token, exchanged under its
// own key (ADR 0072), from the source it answers beside it. It reads this service's operator GRN,
// the audience of every authority it accepts, from the client_id file.
func garamClient(o options) (*http.Client, *machinetoken.Source, string, error) {
	if o.garamURL == "" || o.serverRootFile == "" || o.operatorKeys.PrivateKey == "" ||
		o.operatorKeys.ClientID == "" || o.operatorKeys.TokenURL == "" {
		return nil, nil, "", errors.New("--garam-machine-url, --garam-server-root-file, --operator-private-key-file, " +
			"--operator-client-id-file and --operator-token-url-file are all required")
	}
	audience, err := machinetoken.ReadClientID(o.operatorKeys.ClientID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read this service's operator GRN: %w", err)
	}
	root, err := os.ReadFile(o.serverRootFile)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read garam server root: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return nil, nil, "", errors.New("garam server root file holds no certificate")
	}
	base := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint:    strings.TrimSuffix(o.garamURL, "/") + garamTokenPath,
		IdentityURL: strings.TrimSuffix(o.garamURL, "/") + garamIdentityPath,
		Files:       o.operatorKeys,
		HTTP:        &http.Client{Timeout: garamTimeout, Transport: base},
		Failed: func(failed *machinetoken.Error) {
			slog.Error("exchange a client assertion for a garam token", "reason", failed.Reason,
				"until", failed.Until, "error", failed.Err)
		},
	})
	client := &http.Client{Timeout: garamTimeout, Transport: &machinetoken.Transport{Source: source, Base: base}}
	return client, source, audience, nil
}
