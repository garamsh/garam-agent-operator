// Command gaon-guest is the supervisor that is a gaon MicroVM's
// entrypoint: it runs the agent runtime, the workspace and garam's
// adapter as one generation (ADR-0151 §3).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := guest.FromEnv(os.Getenv, os.Stdout, os.Stderr, log)
	if err == nil {
		err = guest.Run(ctx, cfg)
	}
	if err != nil {
		log.Error("gaon-guest stopped", slog.Any("error", err))
		stop()
		os.Exit(1)
	}
}
