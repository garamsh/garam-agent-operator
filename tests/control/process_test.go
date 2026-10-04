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
		return nil, newSetupError("start "+name, err)
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
			return newSetupError(p.name+" ready", fmt.Errorf("exited before %s answered: %v", url, p.err), p)
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
	return newSetupError(p.name+" ready", err, p)
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
// it involved, read when the failure happened: the suite removes its logs before TestMain prints.
type setupError struct {
	step  string
	err   error
	tails []string
}

func newSetupError(name string, err error, processes ...*process) *setupError {
	e := &setupError{step: name, err: err}
	e.attach(processes...)
	return e
}

// attach records the processes' last log lines now.
func (e *setupError) attach(processes ...*process) {
	for _, p := range processes {
		e.tails = append(e.tails, fmt.Sprintf("--- last %d lines of %s (%s):\n%s", tailLines, p.name, p.log, p.tail()))
	}
}

func (e *setupError) Error() string {
	head := fmt.Sprintf("TestMain setup step %q failed: %v", e.step, e.err)
	return strings.Join(append([]string{head}, e.tails...), "\n")
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
		named.attach(processes...)
		return named
	}
	return newSetupError(name, err, processes...)
}
