//go:build e2e

// Package control_test runs the built control binary against a PostgreSQL container
// and reads the database it set up.
package control_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
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
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/control").CombinedOutput(); err != nil {
		return 0, step("build control binary", fmt.Errorf("%w: %s", err, out))
	}

	container, err := startPostgres(ctx)
	defer func() { _ = testcontainers.TerminateContainer(container) }()
	if err != nil {
		return 0, step("start postgres", err)
	}
	databaseURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, step("read postgres connection string", err)
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
	detachedCmd := exec.Command(binary, controlArgs...)
	detachedCmd.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+databaseURL)
	detached, err := startProcess("detached control", dir, detachedCmd)
	if err != nil {
		return 0, err
	}
	defer detached.stop()
	if err := detached.waitReady(healthURL + "/readyz"); err != nil {
		return 0, err
	}

	serverURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	serverURL = strings.Replace(serverURL, "/control?", "/postgres?", 1)
	real, err = startGaram(ctx, os.Getenv("GARAM_BIN_DIR"), os.Getenv("GARAM_MIGRATIONS_DIR"), serverURL, dir)
	if err != nil {
		return 0, err
	}
	defer real.stop()
	attached, err := startAttached(binary, dir, id, real)
	if err != nil {
		return 0, err
	}
	defer attached.stop()

	pool, err = pgxpool.New(ctx, databaseURL)
	if err != nil {
		return 0, step("open the test's own pool", err)
	}
	defer pool.Close()
	return m.Run(), nil
}

// waitReady polls url until it answers 200, for a binary a test started itself.
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

// postgresStartAttempts bounds how many host ports startPostgres tries.
const postgresStartAttempts = 3

// startPostgres starts the suite's PostgreSQL, published on a loopback port below the kernel's
// ephemeral range. Docker otherwise picks the host port inside that range, where any outgoing
// connection on the host, including the many left in TIME-WAIT, may already hold it, and rootless
// Docker then fails the container's start with "address already in use". A port below the range
// is taken only by an explicit bind, so another attempt is made only when one took it first.
func startPostgres(ctx context.Context) (*postgres.PostgresContainer, error) {
	var lastErr error
	for range postgresStartAttempts {
		port, err := portBelowEphemeralRange()
		if err != nil {
			return nil, err
		}
		container, err := postgres.Run(ctx, postgresImage,
			postgres.WithDatabase("control"),
			postgres.WithUsername("control"),
			postgres.WithPassword("control"),
			postgres.BasicWaitStrategies(),
			testcontainers.WithHostConfigModifier(func(hc *dockercontainer.HostConfig) {
				hc.PortBindings = dockernetwork.PortMap{dockernetwork.MustParsePort("5432/tcp"): {{
					HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: strconv.Itoa(port),
				}}}
			}),
		)
		if err == nil || !strings.Contains(err.Error(), "address already in use") {
			return container, err
		}
		_ = testcontainers.TerminateContainer(container)
		lastErr = fmt.Errorf("port %d: %w", port, err)
	}
	return nil, fmt.Errorf("every one of %d host ports was taken: %w", postgresStartAttempts, lastErr)
}

// portBelowEphemeralRange is a loopback port nothing listens on, between 20000 and the first
// port of the kernel's ephemeral range.
func portBelowEphemeralRange() (int, error) {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, fmt.Errorf("ip_local_port_range holds %q", raw)
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil || low <= 20001 {
		return 0, fmt.Errorf("ephemeral range starts at %q, leaving no port below it", fields[0])
	}
	for range 100 {
		port := 20000 + rand.IntN(low-20000)
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = l.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port between 20000 and %d", low)
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
func startAttached(binary, dir string, id identity, g *garamStack) (*process, error) {
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
	attached, err := startProcess("attached control", dir, control)
	if err != nil {
		return nil, err
	}
	if err := attached.waitReady("http://" + probeAddr + "/readyz"); err != nil {
		attached.stop()
		return nil, step("attached control ready", err, g.processes...)
	}
	return attached, nil
}
