package guest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"syscall"

	"github.com/garamsh/garam/lib/errorx"
)

// running is a started program and the end of its wait.
type running struct {
	program Program
	cmd     *exec.Cmd
	done    chan struct{}
}

// Run makes the directories private, starts the programs in order,
// opens the readiness listener once all of them run, and holds until
// ctx ends or a program exits. Either ends the generation: the listener
// closes, and each program still running is sent SIGTERM and awaited,
// in start order, with no deadline and no kill. It returns nil when ctx
// ended it and an error naming the program when one exited or failed to
// start.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.Programs) == 0 {
		return errorx.New(errorx.KindInvalidArgument, "no program to supervise")
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for _, d := range cfg.Prepare {
		if err := prepare(d); err != nil {
			return err
		}
	}
	exits := make(chan *running, len(cfg.Programs))
	var started []*running
	for _, p := range cfg.Programs {
		r, err := start(p, cfg, exits)
		if err != nil {
			log.Error("program did not start", slog.String("program", p.Name), slog.Any("error", err))
			drain(started, log)
			return err
		}
		log.Info("started", slog.String("program", p.Name), slog.Int("pid", r.cmd.Process.Pid))
		started = append(started, r)
	}

	ln, err := net.Listen("tcp", cfg.ReadyAddr)
	if err != nil {
		drain(started, log)
		return errorx.Wrap(err, "listen for readiness", errorx.KindUnavailable)
	}
	go answer(ln)
	log.Info("ready", slog.String("addr", ln.Addr().String()))

	var cause error
	select {
	case <-ctx.Done():
		log.Info("draining: asked to stop")
	case r := <-exits:
		cause = errorx.New(errorx.KindInternal, fmt.Sprintf("program %s exited: %v", r.program.Name, r.cmd.ProcessState))
		log.Error("draining: a program exited", slog.String("program", r.program.Name), slog.Any("error", cause))
	}
	_ = ln.Close()
	drain(started, log)
	return cause
}

func start(p Program, cfg Config, exits chan<- *running) (*running, error) {
	cmd := exec.Command(p.Path, p.Args...)
	cmd.Env = p.Env
	if p.Cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: p.Cred}
	}
	cmd.Stdout, cmd.Stderr = cfg.Stdout, cfg.Stderr
	if err := cmd.Start(); err != nil {
		return nil, errorx.Wrap(err, "start "+p.Name, errorx.KindInternal)
	}
	r := &running{program: p, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(r.done)
		exits <- r
	}()
	return r, nil
}

// drain stops the programs one after another in the order given,
// waiting for each to exit before signalling the next.
func drain(programs []*running, log *slog.Logger) {
	for _, r := range programs {
		select {
		case <-r.done:
			continue
		default:
		}
		log.Info("terminating", slog.String("program", r.program.Name))
		if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Warn("signal failed", slog.String("program", r.program.Name), slog.Any("error", err))
		}
		<-r.done
		log.Info("exited", slog.String("program", r.program.Name), slog.Any("status", r.cmd.ProcessState))
	}
}

// prepare creates d if absent and gives it to UID at its mode. A path
// that is a symbolic link is refused: the volume it sits on is not the
// supervisor's to trust.
func prepare(d Dir) error {
	if err := os.Mkdir(d.Path, d.Mode); err != nil && !errors.Is(err, os.ErrExist) {
		return errorx.Wrap(err, "prepare "+d.Path, errorx.KindInternal)
	}
	fi, err := os.Lstat(d.Path)
	if err != nil {
		return errorx.Wrap(err, "prepare "+d.Path, errorx.KindInternal)
	}
	if !fi.IsDir() {
		return errorx.New(errorx.KindInvalidArgument, d.Path+" is not a directory")
	}
	if err := os.Lchown(d.Path, UID, UID); err != nil {
		return errorx.Wrap(err, "own "+d.Path, errorx.KindInternal)
	}
	if err := os.Chmod(d.Path, d.Mode); err != nil {
		return errorx.Wrap(err, "chmod "+d.Path, errorx.KindInternal)
	}
	return nil
}

// answer accepts and closes connections: a TCP probe asks only whether
// the port answers.
func answer(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}
