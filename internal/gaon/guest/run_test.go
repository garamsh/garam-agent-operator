package guest_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
)

const (
	envHelper = "GUEST_TEST_HELPER"
	envEvents = "GUEST_TEST_EVENTS"
	envMode   = "GUEST_TEST_MODE"
	modeExit  = "exit"
	drainTime = 150 * time.Millisecond
)

// TestMain turns the test binary into the programs Run supervises: a
// child named by envHelper records "up", waits for SIGTERM, records
// "term", takes drainTime to stop, records "exit".
func TestMain(m *testing.M) {
	if name := os.Getenv(envHelper); name != "" {
		helper(name)
		return
	}
	os.Exit(m.Run())
}

func helper(name string) {
	events := os.Getenv(envEvents)
	record := func(event string) {
		f, err := os.OpenFile(events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = f.WriteString(event + " " + name + "\n")
		_ = f.Close()
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	record("up")
	if os.Getenv(envMode) == modeExit {
		// Leave only once the peers are up, so a SIGTERM to them finds
		// their handler installed.
		deadline := time.Now().Add(10 * time.Second)
		for countLines(events, "up") < 3 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(3)
	}
	<-term
	record("term")
	time.Sleep(drainTime)
	record("exit")
}

func countLines(path, prefix string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, prefix+" ") {
			n++
		}
	}
	return n
}

// syncBuffer is a log sink the test reads while Run still writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// generation is one Run over three helper programs.
type generation struct {
	cfg    guest.Config
	events string
	log    *syncBuffer
}

func newGeneration(t *testing.T, modes map[string]string) generation {
	t.Helper()
	events := filepath.Join(t.TempDir(), "events")
	logs := &syncBuffer{}
	programs := make([]guest.Program, 0, 3)
	for _, name := range []string{"runtime", "workspace", "adapter"} {
		programs = append(programs, guest.Program{
			Name: name,
			Path: os.Args[0],
			Env:  []string{envHelper + "=" + name, envEvents + "=" + events, envMode + "=" + modes[name]},
		})
	}
	return generation{
		cfg: guest.Config{
			Programs:  programs,
			ReadyAddr: freeAddr(t),
			Log:       slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{ReplaceAttr: dropTime})),
		},
		events: events,
		log:    logs,
	}
}

func dropTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// events returns the helpers' records in the order they were written.
func (g generation) recorded(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(g.events)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// started returns the programs the supervisor logged as started, in
// the order it logged them.
func (g generation) started() []string {
	var names []string
	for line := range strings.SplitSeq(g.log.String(), "\n") {
		if program, ok := strings.CutPrefix(line, "level=INFO msg=started program="); ok {
			names = append(names, strings.Fields(program)[0])
		}
	}
	return names
}

// drained returns the supervisor's drain steps as it logged them.
func (g generation) drained() []string {
	var steps []string
	for line := range strings.SplitSeq(g.log.String(), "\n") {
		for _, step := range []string{"terminating", "exited"} {
			if prefix := "level=INFO msg=" + step + " program="; strings.HasPrefix(line, prefix) {
				steps = append(steps, step+" "+strings.Fields(strings.TrimPrefix(line, prefix))[0])
			}
		}
	}
	return steps
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	require.Eventually(t, ok, 10*time.Second, 10*time.Millisecond, what)
}

func answers(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func TestRun_StartsInOrderAndDrainsInThatOrderWaitingForEach(t *testing.T) {
	g := newGeneration(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- guest.Run(ctx, g.cfg) }()

	eventually(t, "readiness answers", func() bool { return answers(g.cfg.ReadyAddr) })
	eventually(t, "all three programs are up", func() bool { return countLines(g.events, "up") == 3 })
	assert.Equal(t, []string{"runtime", "workspace", "adapter"}, g.started())

	cancel()
	require.NoError(t, <-result)
	assert.Equal(t, []string{
		"term runtime", "exit runtime",
		"term workspace", "exit workspace",
		"term adapter", "exit adapter",
	}, withoutUp(g.recorded(t)))
	assert.False(t, answers(g.cfg.ReadyAddr), "the readiness port closes with the drain")
}

func withoutUp(events []string) []string {
	var out []string
	for _, e := range events {
		if !strings.HasPrefix(e, "up ") {
			out = append(out, e)
		}
	}
	return out
}

func TestRun_DrainsTheRestInOrderWhenOneProgramExitsEarly(t *testing.T) {
	g := newGeneration(t, map[string]string{"workspace": modeExit})

	err := guest.Run(context.Background(), g.cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "program workspace exited")
	assert.Equal(t, []string{
		"term runtime", "exit runtime",
		"term adapter", "exit adapter",
	}, withoutUp(g.recorded(t)), "the program that exited is not signalled, the others drain in order")
	assert.False(t, answers(g.cfg.ReadyAddr))
}

func TestRun_NeverOpensReadinessWhenAProgramDoesNotStart(t *testing.T) {
	g := newGeneration(t, nil)
	g.cfg.Programs[2].Path = filepath.Join(t.TempDir(), "missing")

	err := guest.Run(context.Background(), g.cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "start adapter")
	assert.False(t, answers(g.cfg.ReadyAddr), "ready means all three run")
	assert.Equal(t, []string{"runtime", "workspace"}, g.started())
	assert.Equal(t, []string{
		"terminating runtime", "exited runtime",
		"terminating workspace", "exited workspace",
	}, g.drained(), "what had started is drained in order")
}

func TestRun_StartsNothingWhenADirectoryIsASymbolicLink(t *testing.T) {
	g := newGeneration(t, nil)
	link := filepath.Join(t.TempDir(), "memory")
	require.NoError(t, os.Symlink(t.TempDir(), link))
	g.cfg.Prepare = []guest.Dir{{Path: link, Mode: 0o700}}

	err := guest.Run(context.Background(), g.cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
	assert.Empty(t, g.started(), "no program starts on a store the supervisor cannot trust")
}
