//go:build e2e

package control_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tailLines is how many of a process's last log lines a setup failure shows.
const tailLines = 40

// process is a command the suite started, with the file its output goes to.
type process struct {
	name   string
	log    string
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
}

// startProcess starts cmd under name, sending its output to a log file of its own in dir.
func startProcess(name, dir string, cmd *exec.Cmd) (*process, error) {
	log := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".log")
	out, err := os.Create(log)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = out, out
	p := &process{name: name, log: log, cmd: cmd, exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		return nil, &setupError{step: "start " + name, err: err}
	}
	go func() {
		p.err = cmd.Wait()
		_ = out.Close()
		close(p.exited)
	}()
	return p, nil
}

// stop kills the process and waits for it to exit.
func (p *process) stop() {
	_ = p.cmd.Process.Kill()
	<-p.exited
}

// waitReady polls url until it answers 200. It stops as soon as the process exits, since then
// nothing will answer, and fails naming the process and showing its last log lines.
func (p *process) waitReady(url string) error {
	deadline := time.Now().Add(readyTimeout)
	var last string
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			return &setupError{step: p.name + " ready", err: fmt.Errorf("exited before %s answered: %v", url, p.err),
				processes: []*process{p}}
		default:
		}
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("answered %d", resp.StatusCode)
		} else {
			last = err.Error()
		}
		time.Sleep(100 * time.Millisecond)
	}
	err := fmt.Errorf("%s not ready within %s; last: %s", url, readyTimeout, last)
	return &setupError{step: p.name + " ready", err: err, processes: []*process{p}}
}

// tail is the process's last tailLines log lines.
func (p *process) tail() string {
	out, err := os.ReadFile(p.log)
	if err != nil {
		return "(log unreadable: " + err.Error() + ")"
	}
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n"))
	return string(bytes.Join(lines[max(0, len(lines)-tailLines):], []byte("\n")))
}

// setupError is a failure of one TestMain setup step, with the last log lines of the processes
// it involved, so a failure seen once can be diagnosed from its output alone.
type setupError struct {
	step      string
	err       error
	processes []*process
}

func (e *setupError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "TestMain setup step %q failed: %v", e.step, e.err)
	for _, p := range e.processes {
		fmt.Fprintf(&b, "\n--- last %d lines of %s (%s):\n%s", tailLines, p.name, p.log, p.tail())
	}
	return b.String()
}

func (e *setupError) Unwrap() error { return e.err }

// step names err as a failure of the setup step, unless it already names one, and attaches the
// processes whose logs bear on it.
func step(name string, err error, processes ...*process) error {
	if err == nil {
		return nil
	}
	var named *setupError
	if errors.As(err, &named) {
		named.processes = append(named.processes, processes...)
		return named
	}
	return &setupError{step: name, err: err, processes: processes}
}
