// Command control runs the control service: it opens its PostgreSQL store, applies the
// store's schema, and serves its health until it is stopped.
package main

import (
	"context"
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

	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

// databaseURLVariable names the environment variable holding the store's connection URL,
// which carries a password and so is not a flag.
const databaseURLVariable = "CONTROL_DATABASE_URL"

// shutdownTimeout bounds how long the health server drains on stop.
const shutdownTimeout = 5 * time.Second

func main() {
	probeAddr := flag.String("health-probe-bind-address", ":8081", "The address the health endpoints bind to.")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *probeAddr, os.Getenv(databaseURLVariable)); err != nil {
		slog.Error("control service stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, probeAddr, databaseURL string) error {
	if databaseURL == "" {
		return fmt.Errorf("%s is not set", databaseURLVariable)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer pool.Close()

	if err := repository.NewPostgres(pool).ApplySchema(ctx); err != nil {
		return err
	}
	slog.Info("store schema applied")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "store unreachable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Addr: probeAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	slog.Info("serving health", "address", probeAddr)

	select {
	case err := <-served:
		return fmt.Errorf("serve health: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("stop health server: %w", err)
	}
	return nil
}
