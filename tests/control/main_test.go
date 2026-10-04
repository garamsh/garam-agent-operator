//go:build e2e

// Package control_test runs the built control binary against a PostgreSQL container
// and reads the database it set up.
package control_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postgresImage is moved by hand, because no file Dependabot reads holds it. The
// PM moves it and reviews it at each promotion to main.
const postgresImage = "postgres:18.6-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"

// garamURL is the machine listener the binary is pointed at. Until garam can be brought up
// for this suite (issue #230), nothing answers there.
const garamURL = "https://127.0.0.1:1"

// readyTimeout bounds how long the binary has to apply its schema and answer ready.
const readyTimeout = 30 * time.Second

var (
	// pool is the test's own connection to the database the binary opened.
	pool *pgxpool.Pool
	// healthURL is the base URL of the binary's health endpoints.
	healthURL string
	// apiURL is the base URL of the binary's console routes, served over TLS.
	apiURL string
	// apiClient trusts the root that signed the binary's serving certificate.
	apiClient *http.Client
	// binaryPath, controlArgs and databaseURL are what the binary was started with.
	binaryPath  string
	controlArgs []string
	databaseURL string
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "control-e2e-")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binary := filepath.Join(dir, "control")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/control")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return 0, fmt.Errorf("build control binary: %w", err)
	}

	container, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("control"),
		postgres.WithUsername("control"),
		postgres.WithPassword("control"),
		postgres.BasicWaitStrategies(),
	)
	defer func() { _ = testcontainers.TerminateContainer(container) }()
	if err != nil {
		return 0, fmt.Errorf("start postgres: %w", err)
	}
	databaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}

	probeAddr, err := freeAddress()
	if err != nil {
		return 0, err
	}
	apiAddr, err := freeAddress()
	if err != nil {
		return 0, err
	}
	healthURL, apiURL = "http://"+probeAddr, "https://"+apiAddr
	id, err := writeIdentity(dir)
	if err != nil {
		return 0, err
	}
	apiClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: id.roots}}}
	binaryPath, controlArgs = binary, []string{
		"--health-probe-bind-address", probeAddr,
		"--api-bind-address", apiAddr,
		"--api-certificate-file", id.servingCertificate,
		"--api-key-file", id.servingKey,
		"--garam-machine-url", garamURL,
		"--garam-server-root-file", id.serverRoot,
		"--operator-certificate-file", id.certificate,
		"--operator-key-file", id.key,
	}
	control := exec.Command(binary, controlArgs...)
	control.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
	control.Stdout, control.Stderr = os.Stdout, os.Stderr
	if err := control.Start(); err != nil {
		return 0, fmt.Errorf("start control binary: %w", err)
	}
	defer func() {
		_ = control.Process.Kill()
		_ = control.Wait()
	}()
	if err := waitReady(healthURL + "/readyz"); err != nil {
		return 0, err
	}

	pool, err = pgxpool.New(ctx, databaseURL)
	if err != nil {
		return 0, err
	}
	defer pool.Close()
	return m.Run(), nil
}

// waitReady polls url until it answers 200, which the binary does only after applying its schema.
func waitReady(url string) error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("control binary not ready at %s within %s", url, readyTimeout)
}

func freeAddress() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String(), nil
}
