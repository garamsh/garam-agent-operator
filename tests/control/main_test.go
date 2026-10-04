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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postgresImage is moved by hand, because no file Dependabot reads holds it. The
// PM moves it and reviews it at each promotion to main.
const postgresImage = "postgres:18.6-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"

// garamURL is the machine listener the detached binary is pointed at, where nothing answers.
// The attached binary is pointed at the real garam the suite brings up.
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
	// controllerClient is apiClient presenting a controller's client certificate.
	controllerClient *http.Client
	// real is the garam the attached binary calls, and attachedURL that binary's console and
	// controller routes.
	real        *garamStack
	attachedURL string
	// binaryPath, controlArgs and databaseURL are what the detached binary was started with.
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
	controllerClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: id.roots, Certificates: []tls.Certificate{id.controller},
	}}}
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

	serverURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	serverURL = strings.Replace(serverURL, "/control?", "/postgres?", 1)
	real, err = startGaram(ctx, os.Getenv("GARAM_BIN_DIR"), os.Getenv("GARAM_MIGRATIONS_DIR"), serverURL, dir)
	if err != nil {
		return 0, fmt.Errorf("start garam: %w", err)
	}
	defer real.stop()
	stopAttached, err := startAttached(binary, id, real)
	if err != nil {
		return 0, err
	}
	defer stopAttached()

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

// startAttached starts a second control binary on the same database, calling the real garam as
// the hosted operator garam enrolled, and serving under the suite's serving certificate.
func startAttached(binary string, id identity, g *garamStack) (func(), error) {
	probeAddr, err := freeAddress()
	if err != nil {
		return nil, err
	}
	apiAddr, err := freeAddress()
	if err != nil {
		return nil, err
	}
	attachedURL = "https://" + apiAddr
	control := exec.Command(binary,
		"--health-probe-bind-address", probeAddr,
		"--api-bind-address", apiAddr,
		"--api-certificate-file", id.servingCertificate,
		"--api-key-file", id.servingKey,
		"--garam-machine-url", g.machineURL,
		"--garam-server-root-file", g.hostedFiles.serverRoot,
		"--operator-certificate-file", g.hostedFiles.certificate,
		"--operator-key-file", g.hostedFiles.key,
	)
	control.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
	control.Stdout, control.Stderr = os.Stdout, os.Stderr
	if err := control.Start(); err != nil {
		return nil, fmt.Errorf("start attached control binary: %w", err)
	}
	stop := func() {
		_ = control.Process.Kill()
		_ = control.Wait()
	}
	if err := waitReady("http://" + probeAddr + "/readyz"); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}
