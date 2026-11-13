// Package cli is the operator's entry point. Run reads the settings,
// builds the clients of garam, the control service and gaon, and drives
// the reconciler until its context ends.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/config"
	"github.com/garamsh/garam-agent-operator/internal/gaon/health"
	"github.com/garamsh/garam-agent-operator/internal/gaon/loop"
	"github.com/garamsh/garam/lib/errorx"
)

const (
	minBackoff      = time.Second
	maxBackoff      = time.Minute
	shutdownTimeout = 10 * time.Second
)

// Run loads the settings, assembles the operator and reconciles until
// ctx is done, then returns nil. It prints the reason to stderr once
// when it cannot start or when its health listener fails, so a
// deployment's log names it.
func Run(ctx context.Context, getenv func(string) string, stderr io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	op, err := assemble(cfg, log)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return err
	}
	if err := op.run(ctx, cfg.HealthAddr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return err
	}
	return nil
}

// operator is the assembled reconciler with what drives and watches it.
type operator struct {
	service agent.Service
	settle  func()
	probe   *health.Probe
	log     *slog.Logger
}

func (o *operator) run(ctx context.Context, healthAddr string) error {
	ln, err := net.Listen("tcp", healthAddr)
	if err != nil {
		return errorx.Wrap(err, "listen for health probes", errorx.KindUnavailable)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	srv := &http.Server{Handler: o.probe.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			cancel(errorx.Wrap(err, "serve health probes", errorx.KindInternal))
		}
	}()
	o.log.Info("gaon-operator started", slog.String("health", ln.Addr().String()))

	_ = loop.Run(ctx, loop.Options{
		Service: o.service, Settle: o.settle, Observe: o.probe.Observe,
		MinBackoff: minBackoff, MaxBackoff: maxBackoff, Logger: o.log,
	})

	stop, release := context.WithTimeout(context.Background(), shutdownTimeout)
	defer release()
	if err := srv.Shutdown(stop); err != nil {
		o.log.Error("shut down health listener", slog.String("error", err.Error()))
	}
	o.log.Info("gaon-operator stopped")
	if cause := context.Cause(ctx); !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}
