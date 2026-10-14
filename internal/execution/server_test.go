package execution_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
	"github.com/garamsh/garam-agent-operator/internal/execution"
)

// The refusal kinds agent-execution.v2 lists, as these tests expect them.
const (
	kindInvalidRequest       = "invalid_request"
	kindUnauthenticated      = "unauthenticated"
	kindPlacementNotCurrent  = "placement_not_current"
	kindCredentialFenced     = "credential_fenced"
	kindNotAuthorized        = "not_authorized"
	kindRequestReused        = "request_reused"
	kindEpochSuperseded      = "epoch_superseded"
	kindActivationSuperseded = "activation_superseded"
	kindGenerationNotCurrent = "generation_not_current"
	kindUndecided            = "undecided"

	// firstActivation is the activation the garam double makes first.
	firstActivation = "activation-1"

	// keyConfigRevision and keyEpoch are members of both routes' bodies.
	keyConfigRevision = "configRevision"
	keyEpoch          = "epoch"
	keyGeneration     = "generation"
)

const (
	agent      = "grn:acme:default:agent:a"
	controller = "grn:acme:default:operator:k8s"
	epoch      = "7"
	token      = "placement-token-1"
	createRef  = "create-ref"
	generation = "0123456789abcdef0123456789abcdef"
)

// registrar registers the creation of agent at epoch.
type registrar struct{}

func (registrar) Register(context.Context, definition.Registration) (definition.Registered, error) {
	return definition.Registered{Agent: agent, Epoch: epoch}, nil
}

// activation is one activation the garam double holds.
type activation struct {
	id         string
	generation string
	version    int
	ended      bool
}

// garam is the test double for garam's execution fence. It holds activations as activateAgent
// does: a replay of one requestId answers the same activation at a higher token version, the anchor
// must name the agent's latest activation, and an ended one refuses. A token is the agent's current
// one unless fenced, and garam refuses one it holds as foreign as not the agent's token for control,
// as it answers a token minted for garam (422).
type garam struct {
	mu          sync.Mutex
	fenced      map[string]bool
	foreign     map[string]bool
	assignee    string
	epoch       string
	byRequest   map[string]*activation
	latest      string
	active      *activation
	refusedRefs map[string]bool
	introspect  error
	activate    error
	// wrongGRN, where set, is the agent garam's activation answer names; delay holds every
	// activation that long.
	wrongGRN string
	delay    time.Duration
	// generationAnswer, where set, is the standing introspection answers for a supplied generation.
	generationAnswer string
	calls            []execution.ActivationCall
	issued           int
}

func newGaram() *garam {
	return &garam{fenced: map[string]bool{}, foreign: map[string]bool{}, assignee: controller, epoch: epoch,
		byRequest: map[string]*activation{}, refusedRefs: map[string]bool{}}
}

func (g *garam) Introspect(_ context.Context, grn string, token execution.AgentToken, gen string) (execution.Introspection, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.introspect != nil {
		return execution.Introspection{}, g.introspect
	}
	if g.foreign[string(token)] {
		return execution.Introspection{}, fmt.Errorf("%w: garam answered 422", execution.ErrCredentialRefused)
	}
	i := execution.Introspection{GRN: grn, Assignee: g.assignee, Epoch: g.epoch, Mode: "fenced", Credential: "current"}
	if g.fenced[string(token)] {
		i.Credential = "fenced"
	}
	if g.active != nil && !g.active.ended {
		i.ActivationID = g.active.id
	}
	switch {
	case gen != "" && g.generationAnswer != "":
		i.Generation = g.generationAnswer
	case gen == "":
		i.Generation = "not_supplied"
	case i.ActivationID != "" && g.active.generation == gen:
		i.Generation = "current"
	default:
		i.Generation = "not_current"
	}
	return i, nil
}

func (g *garam) Activate(_ context.Context, grn string, call execution.ActivationCall) (execution.Activation, bool, error) {
	g.mu.Lock()
	delay := g.delay
	g.mu.Unlock()
	time.Sleep(delay)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wrongGRN != "" {
		grn = g.wrongGRN
	}
	g.calls = append(g.calls, call)
	if g.activate != nil {
		return execution.Activation{}, false, g.activate
	}
	if g.refusedRefs[call.OperationRef] {
		return execution.Activation{}, false, execution.ErrNotAuthorized
	}
	created := false
	a, ok := g.byRequest[call.RequestID]
	switch {
	case ok && a.ended:
		return execution.Activation{}, false, execution.ErrActivationSuperseded
	case !ok:
		if call.ReplacesActivationID != g.latest {
			return execution.Activation{}, false, execution.ErrActivationSuperseded
		}
		g.issued++
		a = &activation{id: fmt.Sprintf("activation-%d", g.issued), generation: call.Generation}
		g.byRequest[call.RequestID] = a
		g.latest, g.active, created = a.id, a, true
	}
	a.version++
	return execution.Activation{ActivationID: a.id, Token: fmt.Sprintf("token-%s-%d", a.id, a.version),
		TokenVersion: a.version, GRN: grn, Epoch: call.Epoch, Generation: call.Generation}, created, nil
}

// set runs change on the double under its lock.
func (g *garam) set(change func(g *garam)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change(g)
}

// end ends the current activation, as deactivateAgent or a recovered lineage does.
func (g *garam) end() {
	g.set(func(g *garam) {
		if g.active != nil {
			g.active.ended = true
		}
	})
}

func (g *garam) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// caller is an adapter calling the routes: the token it presents in Authorization, none where empty.
type caller struct {
	token string
}

// newCaller is an adapter presenting a fresh token in garam's form.
func newCaller(t *testing.T) caller {
	t.Helper()
	var b [32]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return caller{token: "garam_at_" + hex.EncodeToString(b[:])}
}

// env is the agent routes over an in-memory store holding agent created on controller, configured
// once more, and placed on it under token. adapter presents the agent's token minted for control.
type env struct {
	server      *httptest.Server
	garam       *garam
	definitions definition.Service
	repository  *repository.Memory
	profile     definition.ProfileRef
	adapter     caller
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvLogging(t, slog.New(slog.DiscardHandler))
}

// newEnvLogging is newEnv with the routes logging to logger.
func newEnvLogging(t *testing.T, logger *slog.Logger) *env {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewMemory()
	definitions := definition.NewService(repo, registrar{})
	class := "standard"
	p, err := definitions.PublishProfile(ctx, "acme", "small", definition.ExecutionSettings{
		Resources:   corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		StorageSize: resource.MustParse("1Gi"), StorageClassName: &class,
	})
	require.NoError(t, err)
	profile := definition.ProfileRef{Name: p.Name, Version: p.Version}
	tmpl, err := definitions.PublishTemplate(ctx, "acme", definition.Template{Name: "researcher", Profile: profile})
	require.NoError(t, err)
	_, _, err = definitions.CreateAgent(ctx, definition.CreateInput{
		Request:    definition.RequestKey{Organization: "acme", RequestID: "create"},
		Binding:    definition.Binding{Actor: "actor", Operation: "agent:create", Target: controller, OperationRef: createRef},
		Controller: controller,
		Template:   definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version},
		Profile:    profile,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(token))
	_, _, err = definitions.RegisterPlacement(ctx, definition.PlacementInput{
		Agent: agent, Controller: controller,
		Request: definition.PlacementRequest{PodUID: "pod-1", PVCUID: "pvc-1", Epoch: epoch, TokenSHA256: hex.EncodeToString(digest[:])},
	})
	require.NoError(t, err)

	e := &env{garam: newGaram(), definitions: definitions, repository: repo, profile: profile}
	// Over server TLS alone, as the control service's listener requests no client certificate.
	e.server = httptest.NewTLSServer(execution.NewHandler(execution.Config{
		Definitions: definitions, Garam: e.garam, Logger: logger,
	}))
	t.Cleanup(e.server.Close)
	e.adapter = newCaller(t)
	return e
}

// configure stores the agent's next revision, under ref.
func (e *env) configure(t *testing.T, expected definition.Revision, ref string) {
	t.Helper()
	_, err := e.definitions.Configure(context.Background(), definition.ConfigureInput{
		Request: definition.RequestKey{Organization: "acme", RequestID: fmt.Sprintf("configure-%d", expected)},
		Binding: definition.Binding{Actor: "actor", Operation: "agent:configure", Target: agent, OperationRef: ref,
			Assignment: definition.Assignment{Operator: controller, Epoch: epoch}},
		Agent: agent, ExpectedRevision: expected, Profile: e.profile,
	})
	require.NoError(t, err)
}

// answer is a route's answer: its status, its contract header, and its body decoded.
type answer struct {
	status   int
	contract string
	raw      string
	body     map[string]any
}

func (a answer) kind() string {
	kind, _ := a.body["kind"].(string)
	return kind
}

// post sends body to the agent's route as c, under the contract and, where placement is not
// empty, the placement token.
func (e *env) post(t *testing.T, c caller, route, body, placement string, header map[string]string) answer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/v1/agents/"+agent+"/"+route, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Garam-Contract-Version", execution.Contract)
	if c.token != "" {
		req.Header.Set("Authorization", "Garam-Token "+c.token)
	}
	if placement != "" {
		req.Header.Set("Garam-Placement", placement)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := e.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	a := answer{status: resp.StatusCode, contract: resp.Header.Get("Garam-Contract-Version"), raw: string(raw), body: map[string]any{}}
	if len(bytes.TrimSpace(raw)) > 0 {
		require.NoError(t, json.Unmarshal(raw, &a.body), string(raw))
	}
	return a
}

// activationBody is an activation of gen at revision, under requestID.
func activationBody(requestID, gen, revision string) string {
	b, _ := json.Marshal(map[string]string{"requestId": requestID, keyEpoch: epoch, keyGeneration: gen, keyConfigRevision: revision})
	return string(b)
}

// activate sends the activation of gen at revision under requestID, as c and with the placement token.
func (e *env) activate(t *testing.T, c caller, requestID, gen, revision string) answer {
	t.Helper()
	return e.post(t, c, "activations", activationBody(requestID, gen, revision), token, nil)
}
