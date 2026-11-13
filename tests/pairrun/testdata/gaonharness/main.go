//go:build gaonharness

// Command gaonharness starts gaon's built services for the pair run's
// gaon mode: an embedded etcd, gaon-apiserver with
// --authorization-mode=RBAC and mutual TLS, gaon-controllers (also the
// CSR signer), gaon-scheduler and one gaon-node-agent with
// gaon-storage-local over a minimal mvmd that runs nothing.
//
// It follows gaon's tooling/e2e/extensions (build tag extensions),
// which cannot be imported: that package is test files only. This
// module does not build it, and `go mod tidy` does not read it: it
// sits under testdata, which go skips. gaon-build.sh copies it into a
// clone of gaon's tooling/e2e module, where etcd and mvmd's runtime
// API are already required, and builds it there.
//
// It issues the certificates from one CA, writes the administrator's
// and the operator's client certificates and keys and the CA to -out,
// writes the API server's address to -out/address once the Node is
// Admitted and Ready, and serves until SIGTERM, when it stops what it
// started and removes -dir. Every process, socket and file it makes
// carries 1862.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/garamsh/gaon/lib/api"
	"github.com/garamsh/mvmd/lib/runtimeapi/v1alpha2"
)

const (
	tag            = "gaon-1862"
	node           = "node-1862"
	bootstrapToken = "gaonxx.0123456789abcdef"
	// OperatorCN is the operator's identity, ADR-0151 section 6.
	operatorCN = "garam-gaon-operator"
)

func main() {
	dir := flag.String("dir", "", "a short directory for sockets, logs and keys; removed at the end")
	bin := flag.String("bin", "", "the directory holding the built gaon services")
	out := flag.String("out", "", "where the client certificates and the address are written")
	flag.Parse()
	if *dir == "" || *bin == "" || *out == "" {
		fatal("-dir, -bin and -out are required")
	}
	if err := run(*dir, *bin, *out); err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "gaonharness:", msg)
	os.Exit(1)
}

func run(dir, bin, out string) error {
	var stops []func()
	defer func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
		_ = os.RemoveAll(dir)
	}()

	ca, err := newAuthority(dir)
	if err != nil {
		return err
	}
	server, err := ca.issue("apiserver", nil, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return err
	}
	controllers, _ := ca.issue("controllers", []string{"gaon:controllers"}, x509.ExtKeyUsageClientAuth)
	scheduler, _ := ca.issue("scheduler", []string{"gaon:schedulers"}, x509.ExtKeyUsageClientAuth)
	admin, _ := ca.issue("pair-run-admin", []string{"gaon:admins"}, x509.ExtKeyUsageClientAuth)
	operator, err := ca.issue(operatorCN, nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return err
	}
	for name, file := range map[string]string{"ca.crt": ca.certFile, "admin.crt": admin.certFile, "admin.key": admin.keyFile,
		"operator.crt": operator.certFile, "operator.key": operator.keyFile} {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0o600); err != nil {
			return err
		}
	}

	etcdURL, stopEtcd, err := startEtcd(dir)
	if err != nil {
		return err
	}
	stops = append(stops, stopEtcd)

	tokenFile := filepath.Join(dir, "bootstrap-token")
	if err := os.WriteFile(tokenFile, []byte(bootstrapToken+"\n"), 0o600); err != nil {
		return err
	}
	start := func(name, binary string, args ...string) (*service, error) {
		s, err := startService(dir, bin, name, binary, args...)
		if err == nil {
			stops = append(stops, s.stop)
		}
		return s, err
	}
	apiserver, err := start("apiserver", "gaon-apiserver",
		"--listen", "127.0.0.1:0", "--etcd-endpoints", etcdURL,
		"--tls-client-ca", ca.certFile, "--tls-cert", server.certFile, "--tls-key", server.keyFile,
		"--bootstrap-token-file", tokenFile, "--authorization-mode", "RBAC")
	if err != nil {
		return err
	}
	addr, err := apiserver.address()
	if err != nil {
		return err
	}
	root := "https://" + addr
	computeRoot := root + "/apis/" + api.GroupVersion

	httpClient, err := api.NewTLSClient(ca.certFile, admin.certFile, admin.keyFile)
	if err != nil {
		return err
	}
	compute := api.NewClient(computeRoot, httpClient)
	rbac := api.NewClient(root+"/apis/"+api.RBACGroupVersion, httpClient)
	if err := eventually("the API server bootstraps its roles", 30*time.Second, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l, err := rbac.ListClusterRoles(ctx)
		return err == nil && len(l.Items) > 0
	}); err != nil {
		return err
	}

	tlsFlags := func(id identity) []string {
		return []string{"-tls-ca", ca.certFile, "-tls-cert", id.certFile, "-tls-key", id.keyFile}
	}
	if _, err := start("controllers", "gaon-controllers",
		append([]string{"-api", computeRoot, "-signing-cert", ca.certFile, "-signing-key", ca.keyFile}, tlsFlags(controllers)...)...); err != nil {
		return err
	}
	if _, err := start("scheduler", "gaon-scheduler",
		append([]string{"-api", computeRoot, "-interval", "1s"}, tlsFlags(scheduler)...)...); err != nil {
		return err
	}
	m, stopMvmd, err := startMvmd(dir)
	if err != nil {
		return err
	}
	stops = append(stops, stopMvmd)
	certs := filepath.Join(dir, "node-certs")
	if err := os.Mkdir(certs, 0o700); err != nil {
		return err
	}
	if _, err := start("node-agent", "gaon-node-agent",
		"--api-server", computeRoot, "--node-name", node, "--mvmd-socket", m.socket,
		"--bootstrap-token-file", tokenFile, "--cert-dir", certs, "--tls-ca", ca.certFile,
		"--resync-period", "1s", "--plugin-dir", dir, "--storage-plugin", "local"); err != nil {
		return err
	}
	// The storage plugin serves the Volumes of class local, which the
	// operator's MicroVM names, and reads the certificate the agent keeps.
	if _, err := start("storage-local", "gaon-storage-local",
		"--plugin-dir", dir, "--api-server", computeRoot, "--node-name", node, "--mvmd-socket", m.socket,
		"--cert-dir", certs, "--tls-ca", ca.certFile, "--resync-period", "1s"); err != nil {
		return err
	}

	// The administrator admits the Node, as the extension test does: the
	// agent writes the Node's status each second, so a Conflict retries.
	ctx := context.Background()
	if err := eventually("the Node registers and is admitted", 90*time.Second, func() bool {
		n, err := compute.GetNode(ctx, node)
		if err != nil {
			return false
		}
		n.Spec.Admitted = true
		_, err = compute.PutNode(ctx, n)
		return err == nil
	}); err != nil {
		return err
	}
	if err := eventually("the Node is Admitted and Ready", 90*time.Second, func() bool {
		n, err := compute.GetNode(ctx, node)
		if err != nil || n.Status.State != api.NodeStateAdmitted {
			return false
		}
		for _, c := range n.Status.Conditions {
			if c.Type == api.NodeReady && c.Status == api.ConditionTrue {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}

	logs := filepath.Join(out, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return err
	}
	// The address is written last: the run waits for it.
	if err := os.WriteFile(filepath.Join(out, "address"), []byte(addr+"\n"), 0o600); err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	// The services' logs are copied out before the directory goes, for the
	// run to read for a refusal.
	matches, _ := filepath.Glob(filepath.Join(dir, tag+"-*.log"))
	for _, f := range matches {
		if data, err := os.ReadFile(f); err == nil {
			_ = os.WriteFile(filepath.Join(logs, filepath.Base(f)), data, 0o600)
		}
	}
	return nil
}

func eventually(what string, limit time.Duration, f func() bool) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if f() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s: not within %s", what, limit)
}

// identity is a certificate and its key as files.
type identity struct{ certFile, keyFile string }

type authority struct {
	dir      string
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	certFile string
	keyFile  string
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	return n
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

func newAuthority(dir string) (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: tag + "-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	a := &authority{dir: dir, cert: cert, key: key, certFile: filepath.Join(dir, "ca.crt"), keyFile: filepath.Join(dir, "ca.key")}
	if err := os.WriteFile(a.certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return a, writeKey(a.keyFile, key)
}

// issue makes a certificate for cn with the organizations orgs, which
// gaon reads as the user name and the groups, valid for 127.0.0.1 and
// localhost.
func (a *authority) issue(cn string, orgs []string, usage x509.ExtKeyUsage) (identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: orgs},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return identity{}, err
	}
	id := identity{certFile: filepath.Join(a.dir, cn+".crt"), keyFile: filepath.Join(a.dir, cn+".key")}
	if err := os.WriteFile(id.certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return identity{}, err
	}
	return id, writeKey(id.keyFile, key)
}

// service is a gaon service as its own process, named tag-name.
type service struct {
	log  string
	cmd  *exec.Cmd
	done chan struct{}
}

func startService(dir, bin, name, binary string, args ...string) (*service, error) {
	link := filepath.Join(dir, tag+"-"+name)
	if err := os.Symlink(filepath.Join(bin, binary), link); err != nil {
		return nil, err
	}
	logFile := filepath.Join(dir, tag+"-"+name+".log")
	f, err := os.Create(logFile)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(link, args...) //nolint:gosec // the binary the run built
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	s := &service{log: logFile, cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); _ = f.Close(); close(s.done) }()
	return s, nil
}

func (s *service) stop() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

var servingAddress = regexp.MustCompile(`address=(\S+)`)

// address waits for the API server to log where it listens.
func (s *service) address() (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(s.log)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "serving the resource API") {
				if m := servingAddress.FindStringSubmatch(line); m != nil {
					return m[1], nil
				}
			}
		}
		select {
		case <-s.done:
			return "", fmt.Errorf("the API server ended before it served:\n%s", data)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("the API server did not log its address")
}

func ephemeralURL() (url.URL, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return url.URL{}, err
	}
	defer func() { _ = ln.Close() }()
	return url.URL{Scheme: "http", Host: ln.Addr().String()}, nil
}

func startEtcd(dir string) (string, func(), error) {
	cfg := embed.NewConfig()
	cfg.Dir = filepath.Join(dir, tag+"-etcd")
	cfg.LogLevel = "panic"
	cfg.TickMs, cfg.ElectionMs = 10, 100
	client, err := ephemeralURL()
	if err != nil {
		return "", nil, err
	}
	peer, err := ephemeralURL()
	if err != nil {
		return "", nil, err
	}
	cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{client}, []url.URL{client}
	cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peer}, []url.URL{peer}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
	e, err := embed.StartEtcd(cfg)
	if err != nil {
		return "", nil, err
	}
	select {
	case <-e.Server.ReadyNotify():
	case <-time.After(30 * time.Second):
		e.Close()
		return "", nil, fmt.Errorf("etcd did not become ready")
	}
	return client.String(), e.Close, nil
}

// mvmd is the smallest mvmd a Node Agent runs against, the one of
// gaon's extension test: it serves the runtime API on a Unix socket,
// reports the capacity it was given, and starts every Machine it is
// asked to create. It runs nothing.
type mvmd struct {
	v1alpha2.UnimplementedRuntimeServiceServer
	socket   string
	capacity *v1alpha2.NodeCapacity
	mu       sync.Mutex
	machines map[string]*v1alpha2.Machine
	disks    map[string]*v1alpha2.Disk
}

func startMvmd(dir string) (*mvmd, func(), error) {
	m := &mvmd{
		socket:   filepath.Join(dir, tag+"-mvmd.sock"),
		capacity: &v1alpha2.NodeCapacity{Cpus: 8, MemoryBytes: 16 << 30, StateTotalBytes: 100 << 30, StateAvailableBytes: 90 << 30},
		machines: map[string]*v1alpha2.Machine{},
		disks:    map[string]*v1alpha2.Disk{},
	}
	if len(m.socket) >= 100 {
		return nil, nil, fmt.Errorf("the socket path %s is too long for a Unix socket", m.socket)
	}
	lis, err := net.Listen("unix", m.socket)
	if err != nil {
		return nil, nil, err
	}
	g := grpc.NewServer()
	v1alpha2.RegisterRuntimeServiceServer(g, m)
	go func() { _ = g.Serve(lis) }()
	return m, g.Stop, nil
}

func (m *mvmd) GetRuntimeInfo(context.Context, *v1alpha2.GetRuntimeInfoRequest) (*v1alpha2.GetRuntimeInfoResponse, error) {
	return &v1alpha2.GetRuntimeInfoResponse{ApiVersions: []string{"mvmd.runtime.v1alpha2"}, Capacity: m.capacity}, nil
}

func (m *mvmd) PullImage(context.Context, *v1alpha2.PullImageRequest) (*v1alpha2.PullImageResponse, error) {
	return &v1alpha2.PullImageResponse{}, nil
}

func (m *mvmd) CreateMachine(_ context.Context, r *v1alpha2.CreateMachineRequest) (*v1alpha2.CreateMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.machines[r.GetMachineId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "machine exists")
	}
	created := &v1alpha2.Machine{
		Id:        r.GetMachineId(),
		State:     v1alpha2.MachineState_MACHINE_STATE_CREATED,
		Resources: r.GetResources(),
		Addresses: []string{"10.0.0.2"},
		Disks:     r.GetDisks(),
	}
	m.machines[created.Id] = created
	return &v1alpha2.CreateMachineResponse{Machine: proto.Clone(created).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) StartMachine(_ context.Context, r *v1alpha2.StartMachineRequest) (*v1alpha2.StartMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	held.State = v1alpha2.MachineState_MACHINE_STATE_RUNNING
	return &v1alpha2.StartMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) StopMachine(_ context.Context, r *v1alpha2.StopMachineRequest) (*v1alpha2.StopMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	held.State = v1alpha2.MachineState_MACHINE_STATE_STOPPED
	return &v1alpha2.StopMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) DeleteMachine(_ context.Context, r *v1alpha2.DeleteMachineRequest) (*v1alpha2.DeleteMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.machines, r.GetMachineId())
	return &v1alpha2.DeleteMachineResponse{}, nil
}

func (m *mvmd) InspectMachine(_ context.Context, r *v1alpha2.InspectMachineRequest) (*v1alpha2.InspectMachineResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.machines[r.GetMachineId()]
	if !ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_NOT_FOUND, "no such machine")
	}
	return &v1alpha2.InspectMachineResponse{Machine: proto.Clone(held).(*v1alpha2.Machine)}, nil
}

func (m *mvmd) ListMachines(context.Context, *v1alpha2.ListMachinesRequest) (*v1alpha2.ListMachinesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &v1alpha2.ListMachinesResponse{}
	for _, held := range m.machines {
		out.Machines = append(out.Machines, proto.Clone(held).(*v1alpha2.Machine))
	}
	return out, nil
}

// WatchEvents holds the stream open and sends nothing.
func (m *mvmd) WatchEvents(_ *v1alpha2.WatchEventsRequest, stream v1alpha2.RuntimeService_WatchEventsServer) error {
	<-stream.Context().Done()
	return nil
}

func (m *mvmd) RenewDeadline(context.Context, *v1alpha2.RenewDeadlineRequest) (*v1alpha2.RenewDeadlineResponse, error) {
	return &v1alpha2.RenewDeadlineResponse{Status: &v1alpha2.DeadlineStatus{Phase: v1alpha2.DeadlinePhase_DEADLINE_PHASE_NOT_ARMED}}, nil
}

func (m *mvmd) InspectDeadline(context.Context, *v1alpha2.InspectDeadlineRequest) (*v1alpha2.InspectDeadlineResponse, error) {
	return &v1alpha2.InspectDeadlineResponse{Status: &v1alpha2.DeadlineStatus{Phase: v1alpha2.DeadlinePhase_DEADLINE_PHASE_NOT_ARMED}}, nil
}

// The disks gaon-storage-local makes for a Volume. gaon's extension
// test has none, because no MicroVM of it names a Volume; the
// operator's MicroVM names two.

func (m *mvmd) CreateDisk(_ context.Context, r *v1alpha2.CreateDiskRequest) (*v1alpha2.CreateDiskResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.disks[r.GetDiskId()]; ok {
		return nil, v1alpha2.NewError(v1alpha2.OutcomeKind_OUTCOME_KIND_CONFLICT, "disk exists")
	}
	d := &v1alpha2.Disk{Id: r.GetDiskId(), SizeBytes: r.GetSizeBytes()}
	m.disks[d.Id] = d
	return &v1alpha2.CreateDiskResponse{Disk: proto.Clone(d).(*v1alpha2.Disk)}, nil
}

// ListDisks answers every disk with the Machine that holds it, which is
// the one whose attachments name it.
func (m *mvmd) ListDisks(context.Context, *v1alpha2.ListDisksRequest) (*v1alpha2.ListDisksResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &v1alpha2.ListDisksResponse{}
	for _, d := range m.disks {
		held := proto.Clone(d).(*v1alpha2.Disk)
		held.HolderMachineId = ""
		for _, machine := range m.machines {
			for _, a := range machine.GetDisks() {
				if a.GetDiskId() == d.Id {
					held.HolderMachineId = machine.Id
				}
			}
		}
		out.Disks = append(out.Disks, held)
	}
	sort.Slice(out.Disks, func(i, j int) bool { return out.Disks[i].Id < out.Disks[j].Id })
	return out, nil
}

func (m *mvmd) DeleteDisk(_ context.Context, r *v1alpha2.DeleteDiskRequest) (*v1alpha2.DeleteDiskResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.disks, r.GetDiskId())
	return &v1alpha2.DeleteDiskResponse{}, nil
}
