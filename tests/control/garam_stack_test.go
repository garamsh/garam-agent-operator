//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// garamContract is the operation-authority contract version garam's browser API speaks here, and its
// machine listener beside a certificate.
const garamContract = "operation-authority.v1"

// enrollmentContract is garam's agent-enrollment.v1, which its agent admission speaks beside a token
// (garam@37567a4).
const enrollmentContract = "agent-enrollment.v1"

// fenceContract is garam's execution-fence.v2, which its recovery, deactivation and execution
// introspection speak beside a token (garam@afde8b8).
const fenceContract = "execution-fence.v2"

// garamStack is a real garam built at the revision the Makefile pins: its authz engine, its
// browser API and its machine listener, over a database garam's test-principal fixture prepared.
// Everything past that one signed-in user is made through garam's public routes.
type garamStack struct {
	apiURL     string
	machineURL string
	session    *http.Cookie
	// orgGRN and orgID are the organization the signed-in user created, and its identifier.
	orgGRN, orgID string
	// hosted and controllerGRN are the two operators garam enrolled: this service, and the
	// controller agents are assigned to.
	hosted, controllerGRN string
	// hostedFiles are the key garam issued this service, and garam's server root.
	hostedFiles identity
	// controllerToken exchanges, under the key garam issued the controller, the controller's token
	// for this service. It is one source for the suite, as the manager holds one: garam admits ten
	// exchanges a minute per operator.
	controllerToken *machinetoken.Source
	machine         *http.Client
	// controllerMachine and hostedMachine call garam's machine listener as the controller and as
	// this service, each under a token exchanged under its own key: the controller mints agents'
	// tokens, as the manager does, and this service's calls are made where the suite stands in for
	// the binary.
	controllerMachine, hostedMachine *http.Client
	// controller is the certificate garam enrolled the controller with, which the suite presents
	// itself to make on garam what the binary takes as given: its legacy agents.
	controller tls.Certificate
	// processes are garam's three running for the suite.
	processes []*process
}

// stop stops every garam process the suite started.
func (s *garamStack) stop() {
	for _, p := range s.processes {
		p.stop()
	}
}

// startGaram brings garam up on loopback ports over a fresh database on the PostgreSQL server
// serverURL names, signs in the fixture's first user, and makes through garam's public routes an
// organization, this service's hosted operator and one controller, each enrolled, and a
// delegation from the one to the other.
func startGaram(ctx context.Context, binDir, migrations, serverURL, dir string) (_ *garamStack, err error) {
	if binDir == "" || migrations == "" {
		return nil, step("locate garam", errors.New("GARAM_BIN_DIR and GARAM_MIGRATIONS_DIR are unset: "+
			"run the suite through `make test-e2e-control`, which builds garam at GARAM_REVISION"))
	}
	s := &garamStack{}
	defer func() {
		if err != nil {
			s.stop()
		}
	}()

	prepared, err := prepareDatabase(ctx, filepath.Join(binDir, "testprincipal"), migrations, serverURL)
	if err != nil {
		return nil, step("prepare garam's database with testprincipal", err)
	}
	s.session = &http.Cookie{Name: prepared.Session.Cookie, Value: prepared.Session.Token}

	keys, err := writeGaramKeys(dir)
	if err != nil {
		return nil, step("write garam's key material", err)
	}
	addrs := map[string]string{}
	for _, name := range []string{"grpc", "authzMetrics", "http", "machine", "machineHealth"} {
		if addrs[name], err = freeAddress(); err != nil {
			return nil, step("choose garam's ports", err)
		}
	}
	garam := filepath.Join(binDir, "garam")
	// The token endpoint every issued key's assertions name: garam answers no key without it.
	tokenURL := "https://" + addrs["machine"] + "/oauth/token"
	common := []string{"GARAM_DATABASE_URL=" + prepared.DatabaseURL, "GARAM_AUTHZ_ADDR=" + addrs["grpc"],
		"GARAM_MACHINE_TOKEN_URL=" + tokenURL}
	for _, p := range []struct {
		name, ready string
		env         []string
	}{
		{"authz", "http://" + addrs["authzMetrics"] + "/readyz", []string{
			"GARAM_GRPC_ADDR=" + addrs["grpc"], "GARAM_METRICS_ADDR=" + addrs["authzMetrics"]}},
		{"api", "http://" + addrs["http"] + "/readyz", []string{
			"GARAM_HTTP_ADDR=" + addrs["http"], "GARAM_COOKIE_SECURE=false"}},
		{"machine", "http://" + addrs["machineHealth"] + "/readyz", []string{
			"GARAM_MACHINE_ADDR=" + addrs["machine"], "GARAM_MACHINE_HEALTH_ADDR=" + addrs["machineHealth"],
			"GARAM_MACHINE_TLS_CERT_FILE=" + keys.listenerCert, "GARAM_MACHINE_TLS_KEY_FILE=" + keys.listenerKey,
			"GARAM_MACHINE_SERVER_ROOT_FILE=" + keys.serverRoot, "GARAM_CA_KEY_FILE=" + keys.kek,
			// The issuer garam signs messages under, which garam requires and this suite reads nothing of.
			"GARAM_MACHINE_ISSUER=https://" + addrs["machine"]}},
	} {
		cmd := exec.Command(garam, "serve", p.name)
		cmd.Env = append(append(os.Environ(), common...), p.env...)
		started, err := startProcess("garam serve "+p.name, dir, cmd)
		if err != nil {
			return nil, step("start garam serve "+p.name, err, s.processes...)
		}
		s.processes = append(s.processes, started)
		if err := started.waitReady(p.ready); err != nil {
			return nil, step("garam serve "+p.name+" ready", err, s.processes[:len(s.processes)-1]...)
		}
	}
	s.apiURL = "http://" + addrs["http"]
	s.machineURL = "https://" + addrs["machine"]
	s.machine = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: keys.roots}}}

	if err := s.setUp(dir, keys); err != nil {
		return nil, err
	}
	return s, nil
}

// prepared is what the test-principal fixture answers under its contract 1.
type prepared struct {
	Contract    int    `json:"contract"`
	Database    string `json:"database"`
	DatabaseURL string `json:"databaseUrl"`
	UserGRN     string `json:"userGrn"`
	Session     struct {
		Cookie string `json:"cookie"`
		Token  string `json:"token"`
	} `json:"session"`
}

// prepareDatabase runs the fixture as garam's tests/testprincipal/README.md §Invocation states it.
func prepareDatabase(ctx context.Context, testprincipal, migrations, serverURL string) (prepared, error) {
	cmd := exec.CommandContext(ctx, testprincipal, "prepare", "-contract", "1", "-migrations", migrations,
		"-server-url", serverURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return prepared{}, fmt.Errorf("testprincipal prepare: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var p prepared
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil || p.Contract != 1 {
		return prepared{}, fmt.Errorf("testprincipal prepare answered %q under no contract 1", stdout.String())
	}
	return p, nil
}

// garamKeys are the files garam's machine listener is configured with, and the root that
// verifies it.
type garamKeys struct {
	serverRoot, listenerCert, listenerKey, kek string
	roots                                      *x509.CertPool
}

// writeGaramKeys writes a garam server root, the listener certificate it signed for 127.0.0.1,
// and the key-encryption key garam seals its certificate authorities under, each readable by its
// owner alone, as garam requires.
func writeGaramKeys(dir string) (garamKeys, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return garamKeys{}, err
	}
	root := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "garam e2e server root"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(2 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		return garamKeys{}, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return garamKeys{}, err
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(2 * time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		return garamKeys{}, err
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return garamKeys{}, err
	}
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		return garamKeys{}, err
	}
	k := garamKeys{
		serverRoot:   filepath.Join(dir, "garam-server-root.pem"),
		listenerCert: filepath.Join(dir, "garam-listener.pem"),
		listenerKey:  filepath.Join(dir, "garam-listener-key.pem"),
		kek:          filepath.Join(dir, "garam-kek.hex"),
		roots:        x509.NewCertPool(),
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return garamKeys{}, err
	}
	k.roots.AddCert(rootCert)
	for path, content := range map[string][]byte{
		k.serverRoot:   pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: rootDER}),
		k.listenerCert: pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: leafDER}),
		k.listenerKey:  pem.EncodeToMemory(&pem.Block{Type: pemECKey, Bytes: leafKeyDER}),
		k.kek:          []byte(hex.EncodeToString(kek) + "\n"),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return garamKeys{}, err
		}
	}
	return k, nil
}

// setUp makes, through garam's public routes, the organization, the two operators each enrolled
// over a key generated here and issued a key of garam's, and the hosted operator's delegation over
// the controller.
func (s *garamStack) setUp(dir string, keys garamKeys) error {
	var org struct {
		GRN string `json:"grn"`
	}
	if err := s.create(http.MethodPost, "/orgs", "", struct {
		Name string `json:"name"`
	}{Name: "acme"}, &org); err != nil {
		return step("create the organization", err, s.processes...)
	}
	s.orgGRN = org.GRN
	s.orgID = org.GRN[strings.LastIndex(org.GRN, ":")+1:]

	hosted, err := s.enroll("control")
	if err != nil {
		return step("register and enroll the hosted operator", err, s.processes...)
	}
	controller, err := s.enroll("k8s")
	if err != nil {
		return step("register and enroll the controller", err, s.processes...)
	}
	s.hosted, s.controllerGRN, s.controller = hosted.grn, controller.grn, controller.pair
	s.hostedFiles = identity{serverRoot: keys.serverRoot}
	if s.hostedFiles.operatorKeys, err = s.issueKey(dir, "control"); err != nil {
		return step("issue the hosted operator's key", err, s.processes...)
	}
	controllerKeys, err := s.issueKey(dir, "k8s")
	if err != nil {
		return step("issue the controller's key", err, s.processes...)
	}
	s.controllerToken = machinetoken.NewSource(machinetoken.Config{
		Endpoint: s.machineURL + "/oauth/token", Files: controllerKeys, Audience: s.hosted, HTTP: s.machine,
	})
	s.controllerMachine = s.tokenClient(controllerKeys)
	s.hostedMachine = s.tokenClient(s.hostedFiles.operatorKeys)

	delegation := struct {
		RequestID   string   `json:"requestId"`
		Controllers []string `json:"controllers"`
		Operations  []string `json:"operations"`
		ExpiresAt   string   `json:"expiresAt"`
	}{
		RequestID:   "e2e-delegation",
		Controllers: []string{s.controllerGRN},
		Operations: []string{"agent:create", "agent:configure", "agent:activate", "agent:recover", "agent:cutover",
			"agent-template:read", "agent-template:publish", "execution-profile:read", "agent:execution-read",
			"operator:read"},
		ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}
	return step("write the hosted operator's delegation",
		s.create(http.MethodPut, "/orgs/"+s.orgID+"/operators/control/delegation", garamContract, delegation, nil),
		s.processes...)
}

// tokenClient is a client of garam's machine listener presenting the token the operator files name
// exchanges for garam itself.
func (s *garamStack) tokenClient(files machinetoken.Files) *http.Client {
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: s.machineURL + "/oauth/token", Files: files, HTTP: s.machine,
	})
	return &http.Client{Transport: &machinetoken.Transport{Source: source, Base: s.machine.Transport}}
}

// mintAgentToken has the controller mint agent's token for audience, garam's own where it is empty,
// as the manager does (garam@afde8b8 api/machine.yaml mintAgentToken). The token is never logged.
func (s *garamStack) mintAgentToken(agent, audience string) (string, error) {
	var minted struct {
		Token string `json:"token"`
	}
	in := map[string]string{}
	if audience != "" {
		in["audience"] = audience
	}
	err := s.machineCall(s.controllerMachine, "/agents/"+url.PathEscape(agent)+"/tokens", "machine-token.v1",
		in, http.StatusCreated, &minted)
	return minted.Token, err
}

// enrolled is an operator garam signed a certificate for, over a key generated here.
type enrolled struct {
	grn  string
	pair tls.Certificate
}

// issueKey has garam issue the operator under identifier a key, as a person does through the
// console (garam@9ba35c3 api/openapi.yaml issueOperatorKey), and places the answer in dir.
func (s *garamStack) issueKey(dir, identifier string) (machinetoken.Files, error) {
	var issued struct {
		PrivateKeyPEM string `json:"privateKeyPem"`
		ClientID      string `json:"clientId"`
		TokenURL      string `json:"tokenUrl"`
	}
	if err := s.create(http.MethodPost, "/orgs/"+s.orgID+"/operators/"+identifier+"/keys", "", struct {
		Rotation string `json:"rotation"`
	}{Rotation: "immediate"}, &issued); err != nil {
		return machinetoken.Files{}, err
	}
	return writeKeyIssuance(dir, identifier, issued.ClientID, issued.TokenURL, issued.PrivateKeyPEM)
}

// enroll registers an operator under identifier and enrolls it with the token the registration
// answered, as an operator enrolls itself: its key never leaves this process.
func (s *garamStack) enroll(identifier string) (enrolled, error) {
	var registered struct {
		Operator struct {
			GRN string `json:"grn"`
		} `json:"operator"`
		Enrollment struct {
			Token string `json:"token"`
		} `json:"enrollment"`
	}
	if err := s.create(http.MethodPost, "/orgs/"+s.orgID+"/operators", "", struct {
		Identifier string `json:"identifier"`
		Name       string `json:"name"`
	}{Identifier: identifier, Name: identifier}, &registered); err != nil {
		return enrolled{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return enrolled{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return enrolled{}, err
	}
	var issued struct {
		GRN            string `json:"grn"`
		CertificatePEM string `json:"certificatePem"`
		IssuerPEM      string `json:"issuerPem"`
	}
	body := map[string]string{
		"token":                 registered.Enrollment.Token,
		"certificateRequestPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})),
	}
	if err := s.machineCall(s.machine, "/enrollment", "", body, http.StatusCreated, &issued); err != nil {
		return enrolled{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return enrolled{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: pemECKey, Bytes: keyDER})
	pair, err := tls.X509KeyPair([]byte(issued.CertificatePEM), keyPEM)
	if err != nil {
		return enrolled{}, fmt.Errorf("enrolled certificate for %s: %w", identifier, err)
	}
	return enrolled{grn: issued.GRN, pair: pair}, nil
}

// mintAuthority has garam mint, for the signed-in user, an authority handing this service
// operation on target for requestID and the body whose digest is bodySHA256.
func (s *garamStack) mintAuthority(operation, target, requestID, bodySHA256 string) (
	authority, operationRef string, err error,
) {
	return s.mintTargeted(operation, target, requestID, bodySHA256, "")
}

// mintTargeted is mintAuthority binding requestTarget too, the exact origin-form target the
// request is sent to, which garam binds for the reads and the publication (garam@33b1c41).
func (s *garamStack) mintTargeted(operation, target, requestID, bodySHA256, requestTarget string) (
	authority, operationRef string, err error,
) {
	var minted struct {
		Authority    string `json:"authority"`
		OperationRef string `json:"operationRef"`
	}
	err = s.create(http.MethodPost, "/orgs/"+s.orgID+"/operation-authorities", garamContract, struct {
		Audience      string `json:"audience"`
		Operation     string `json:"operation"`
		Target        string `json:"target"`
		RequestID     string `json:"requestId"`
		BodySHA256    string `json:"bodySha256"`
		RequestTarget string `json:"requestTarget,omitempty"`
	}{s.hosted, operation, target, requestID, bodySHA256, requestTarget}, &minted)
	return minted.Authority, minted.OperationRef, err
}

// createAgent has garam admit an agent assigned to the controller, as this service does on the
// console's create: an agent:create authority, then prepareAgentEnrollment and admitAgentEnrollment
// under this service's token. It answers the agent's GRN and its assignment's epoch.
func (s *garamStack) createAgent(requestID string) (agent, epoch string, err error) {
	digest := sha256.Sum256([]byte("{}"))
	_, ref, err := s.mintAuthority("agent:create", s.controllerGRN, requestID, hex.EncodeToString(digest[:]))
	if err != nil {
		return "", "", err
	}
	binding := struct {
		RequestID    string `json:"requestId"`
		Placement    string `json:"placement"`
		Assignee     string `json:"assignee"`
		OperationRef string `json:"operationRef"`
	}{requestID, "managed", s.controllerGRN, ref}
	var approval struct {
		ApprovalID string `json:"approvalId"`
	}
	if err := s.machineCall(s.hostedMachine, "/agent-enrollment-approvals", enrollmentContract, binding,
		http.StatusCreated, &approval); err != nil {
		return "", "", err
	}
	var admitted struct {
		GRN   string `json:"grn"`
		Epoch string `json:"epoch"`
	}
	err = s.machineCall(s.hostedMachine, "/agent-enrollment-approvals/"+url.PathEscape(approval.ApprovalID)+"/admission",
		enrollmentContract, binding, http.StatusCreated, &admitted)
	return admitted.GRN, admitted.Epoch, err
}

// create calls the browser API as the fixture's signed-in user, refusing any answer but 201.
func (s *garamStack) create(method, path, contract string, in any, out any) error {
	return call(http.DefaultClient, method, s.apiURL+path, contract, s.session, in, http.StatusCreated, out)
}

func (s *garamStack) machineCall(client *http.Client, path, contract string, in any, want int, out any) error {
	return call(client, http.MethodPost, s.machineURL+path, contract, nil, in, want, out)
}

// call sends in as JSON and decodes the answer into out, refusing any status but want.
func call(client *http.Client, method, target, contract string, cookie *http.Cookie, in any, want int, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if contract != "" {
		req.Header.Set("Garam-Contract-Version", contract)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s answered %d, want %d: %s", method, target, resp.StatusCode, want, answer)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(answer, out)
}
