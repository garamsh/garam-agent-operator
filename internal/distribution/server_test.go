package distribution_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
)

const (
	controller = "grn:root:default:operator:k8s"
	elsewhere  = "grn:root:default:operator:other"
	org        = "grn:root:default:org:acme"
	agentA     = "grn:acme:default:agent:a"
	agentB     = "grn:acme:default:agent:b"
	agentC     = "grn:acme:default:agent:c"
	epoch      = "7"
)

// verdict is what the test double answers for one proof.
type verdict struct {
	proof distribution.Proof
	err   error
}

// prover is the test double for garam's controller proof. It proves the controller and each
// agent given an epoch, unless the test set another verdict, and records every leaf it was sent.
type prover struct {
	mu       sync.Mutex
	session  *verdict
	agents   map[string]verdict
	epochs   map[string]string
	leaves   [][]byte
	sessions int
}

func (p *prover) Prove(_ context.Context, c string, leafPEM []byte, agent string) (distribution.Proof, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leaves = append(p.leaves, leafPEM)
	if agent == "" {
		p.sessions++
		if p.session != nil {
			return p.session.proof, p.session.err
		}
		return distribution.Proof{Operator: c, Org: org}, nil
	}
	if v, ok := p.agents[agent]; ok {
		return v.proof, v.err
	}
	e, ok := p.epochs[agent]
	if !ok {
		return distribution.Proof{}, distribution.ErrNotProved
	}
	return distribution.Proof{Operator: c, Org: org, Agent: &distribution.ProvenAgent{GRN: agent, Epoch: e}}, nil
}

func (p *prover) setSession(v verdict) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = &v
}

// setAgentB sets the verdict for agentB's placement proofs.
func (p *prover) setAgentB(v verdict) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.agents[agentB] = v
}

// registrar registers each creation under the GRN its request id names.
type registrar struct{}

func (registrar) Register(_ context.Context, r definition.Registration) (definition.Registered, error) {
	return definition.Registered{Agent: definition.GRN(r.Request.RequestID), Epoch: "1"}, nil
}

// creator is the controller the fixture's agents are created on, so revision 1 is no controller's
// under test and each test's revisions come from configure.
const creator = "grn:root:default:operator:creator"

// env is the controller routes over an in-memory store holding agentA and agentB recorded for
// controller under epoch, and agentC recorded for another controller.
type env struct {
	server      *httptest.Server
	prover      *prover
	definitions definition.Service
	profile     definition.ProfileRef
	withCert    *http.Client
	withoutCert *http.Client
	// leaf is the DER of the certificate withCert presents.
	leaf       []byte
	configures int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvCarrying(t, 10)
}

// newEnvCarrying is newEnv with one feed answer carrying at most maxAgents candidates.
func newEnvCarrying(t *testing.T, maxAgents int) *env {
	t.Helper()
	ctx := context.Background()
	definitions := definition.NewService(repository.NewMemory(), registrar{})
	class := "standard"
	p, err := definitions.PublishProfile(ctx, "acme", "small", definition.ExecutionSettings{
		Resources:        corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		StorageSize:      resource.MustParse("1Gi"),
		StorageClassName: &class,
	})
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}
	tmpl, err := definitions.PublishTemplate(ctx, "acme", definition.Template{Name: "researcher", Profile: profile})
	require.NoError(t, err)

	e := &env{
		prover:      &prover{agents: map[string]verdict{}, epochs: map[string]string{agentA: epoch, agentB: epoch}},
		definitions: definitions,
		profile:     profile,
	}
	for _, a := range []string{agentA, agentB, agentC} {
		_, _, err := definitions.CreateAgent(ctx, definition.CreateInput{
			Request:    definition.RequestKey{Organization: "acme", RequestID: a},
			Binding:    definition.Binding{Actor: "actor", Operation: "agent:create", Target: creator},
			Controller: creator,
			Template:   definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version},
			Profile:    profile,
		})
		require.NoError(t, err)
	}
	e.configure(t, agentA, controller, 1)
	e.configure(t, agentB, controller, 1)
	e.configure(t, agentC, elsewhere, 1)

	e.server = httptest.NewUnstartedServer(distribution.NewHandler(distribution.Config{
		Definitions:  definitions,
		Prover:       e.prover,
		PollInterval: 10 * time.Millisecond,
		MaxAgents:    maxAgents,
		Logger:       slog.New(slog.DiscardHandler),
	}))
	e.server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	e.server.StartTLS()
	t.Cleanup(e.server.Close)
	e.withoutCert = e.server.Client()
	e.withCert, e.leaf = clientWithLeaf(t, e.server, controller)
	return e
}

// configure stores agent's next revision, recorded for operator under epoch.
func (e *env) configure(t *testing.T, agent, operator string, expected definition.Revision) {
	t.Helper()
	e.configures++
	_, err := e.definitions.Configure(context.Background(), definition.ConfigureInput{
		Request: definition.RequestKey{Organization: "acme", RequestID: fmt.Sprintf("configure-%d", e.configures)},
		Binding: definition.Binding{
			Actor: "actor", Operation: "agent:configure", Target: agent,
			Assignment: definition.Assignment{Operator: operator, Epoch: epoch},
		},
		Agent:            definition.GRN(agent),
		ExpectedRevision: expected,
		Profile:          e.profile,
		Config:           definition.Configuration{Ego: fmt.Sprintf("revision %d", expected+1)},
	})
	require.NoError(t, err)
}

// clientWithLeaf is a client of server presenting a certificate whose one SAN URI is grn.
func clientWithLeaf(t *testing.T, server *httptest.Server, grn string) (*http.Client, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	san, err := url.Parse(grn)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{san},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	client := server.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}
	return &http.Client{Transport: transport}, der
}

// feed is one answer of the desired feed.
type feed struct {
	status  int
	cursor  string
	agents  map[string]string
	kind    string
	message string
}

func (e *env) desired(t *testing.T, client *http.Client, query string) feed {
	t.Helper()
	resp, err := client.Get(e.server.URL + "/v1/operators/self/desired" + query)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Cursor string `json:"cursor"`
		Agents []struct {
			Agent    string `json:"agent"`
			Revision string `json:"revision"`
		} `json:"agents"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	f := feed{status: resp.StatusCode, cursor: out.Cursor, agents: map[string]string{}, kind: out.Kind, message: out.Message}
	for _, a := range out.Agents {
		f.agents[a.Agent] = a.Revision
	}
	return f
}

// report sends a status report for agent and returns the status and the answer's raw fields.
func (e *env) report(t *testing.T, client *http.Client, agent, body string) (int, map[string]any) {
	t.Helper()
	resp, err := client.Post(e.server.URL+"/v1/operators/self/agents/"+agent+"/status", "application/json",
		strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return resp.StatusCode, out
}
