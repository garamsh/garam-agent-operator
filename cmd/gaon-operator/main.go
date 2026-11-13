// Command gaon-operator is the operator that renders garam's agents
// into gaon (ADR-0151). All wiring lives in internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/garamsh/garam-agent-operator/internal/gaon/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.Run(ctx, os.Getenv, os.Stderr)
	stop()
	if err != nil {
		os.Exit(1)
	}
}
