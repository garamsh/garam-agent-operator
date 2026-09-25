package garam_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

const (
	// pollInterval is short enough that a test sees several passes.
	pollInterval = 20 * time.Millisecond

	// pollTimeout bounds what a test waits for the poller to have done.
	pollTimeout = 5 * time.Second

	claimedAgent   = garam.GRN("grn:acme:default:agent:0a1b2c3d4e5f6071")
	unclaimedAgent = garam.GRN("grn:acme:default:agent:9f2ac1b40d8e7a35")
)

// recordingConstructor stands in for the cluster this operator constructs into,
// which is the module boundary a Poller is written against. It builds the Agent
// before it places the credential, as the constructor does, so that a refused
// placement leaves the two apart the way a real one would.
type recordingConstructor struct {
	mu sync.Mutex

	// built is the agents the cluster carries an Agent for.
	built map[garam.GRN]bool

	// placed is the agents whose credential the cluster already carries.
	placed map[garam.GRN]garam.AgentCredential

	// epochs is the epoch each agent was constructed at, which is what a
	// report to garam later carries.
	epochs map[garam.GRN]int64

	// stale is the agents whose spec carries an image this operator is no longer
	// configured with, which is what a correction clears.
	stale map[garam.GRN]bool

	// declared is the tool set each agent was constructed from, which is what a
	// definition declares and this operator carries rather than decides.
	declared map[garam.GRN]garam.ToolSet

	// refusePlacement is what Construct answers instead of placing the
	// credential, where it is set.
	refusePlacement error
}

func newRecordingConstructor() *recordingConstructor {
	return &recordingConstructor{
		built:    map[garam.GRN]bool{},
		placed:   map[garam.GRN]garam.AgentCredential{},
		epochs:   map[garam.GRN]int64{},
		stale:    map[garam.GRN]bool{},
		declared: map[garam.GRN]garam.ToolSet{},
	}
}

func (c *recordingConstructor) HasCredential(_ context.Context, agent garam.GRN) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, placed := c.placed[agent]
	return placed, nil
}

func (c *recordingConstructor) Construct(_ context.Context, definition garam.Definition, epoch int64, credential garam.AgentCredential) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	agent := definition.Agent
	c.built[agent] = true
	c.epochs[agent] = epoch
	c.declared[agent] = definition.Tools
	if c.refusePlacement != nil {
		return c.refusePlacement
	}
	c.placed[agent] = credential
	return nil
}

func (c *recordingConstructor) CorrectImage(_ context.Context, agent garam.GRN) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stale[agent] {
		return false, nil
	}
	delete(c.stale, agent)
	return true, nil
}

// stillStale is the agents whose image no correction has reached.
func (c *recordingConstructor) stillStale() []garam.GRN {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Collect(maps.Keys(c.stale))
}

// constructedAt is the epoch each agent was constructed at.
func (c *recordingConstructor) constructedAt() map[garam.GRN]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := map[garam.GRN]int64{}
	maps.Copy(held, c.epochs)
	return held
}

// credentials is what the constructor holds, by agent.
func (c *recordingConstructor) credentials() map[garam.GRN]garam.AgentCredential {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := map[garam.GRN]garam.AgentCredential{}
	maps.Copy(held, c.placed)
	return held
}

// toolSets is the tool set each agent was constructed from, by agent.
func (c *recordingConstructor) toolSets() map[garam.GRN]garam.ToolSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := map[garam.GRN]garam.ToolSet{}
	maps.Copy(held, c.declared)
	return held
}

// runPoller polls stub until the test ends, and fails the test where Start
// answers an error rather than stopping quietly.
func runPoller(t *testing.T, stub *stubListener, constructor garam.Constructor) {
	t.Helper()

	poller := garam.NewPoller(garam.NewClient(stub.address(), trustedBy(t, stub)), constructor, pollInterval)
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), testr.New(t)))
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := poller.Start(ctx); err != nil {
			t.Errorf("the poller stopped with %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
}

// claims is the agents the stub was asked to claim, in the order it was asked.
func claims(stub *stubListener) []string {
	var agents []string
	for _, request := range stub.requests() {
		if request.method == http.MethodPost && strings.HasSuffix(request.path, "/claim") {
			agents = append(agents, strings.TrimSuffix(strings.TrimPrefix(request.path, "/definitions/"), "/claim"))
		}
	}
	return agents
}

// certificatesAsked is the agents the stub was asked for a certificate for, in
// the order it was asked.
func certificatesAsked(stub *stubListener) []string {
	var agents []string
	for _, request := range stub.requests() {
		if request.method == http.MethodPost && strings.HasSuffix(request.path, "/certificate") {
			agents = append(agents, strings.TrimSuffix(strings.TrimPrefix(request.path, "/agents/"), "/certificate"))
		}
	}
	return agents
}

// answerDefinitionsAndCertificates serves definitions and the certificate route
// the way garam's listener does, and refuses a certificate for notHeld with the
// 403 garam answers an agent this operator does not hold.
func answerDefinitionsAndCertificates(definitions string, notHeld garam.GRN) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			answerJSON(w, http.StatusOK, definitions)
		case strings.HasSuffix(r.URL.Path, "/certificate"):
			if notHeld != "" && strings.Contains(r.URL.Path, string(notHeld)) {
				answerJSON(w, http.StatusForbidden,
					`{"kind": "permission_denied", "message": "agent is not assigned to this operator"}`)
				return
			}
			answerJSON(w, http.StatusCreated, `{
				"grn": "x", "certificatePem": "a certificate", "privateKeyPem": "a key",
				"issuerPem": "an issuer", "serverRootPem": "a root", "notAfter": "2026-08-26T15:01:43Z"
			}`)
		default:
			answerJSON(w, http.StatusCreated, `{"grn": "x",
				"operator": "grn:acme:default:operator:one", "epoch": 1}`)
		}
	}
}

func TestPollerClaimsTheDefinitionsGaramHoldsNoClaimFor(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			answerJSON(w, http.StatusOK, `[
				{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 1}},
				{"agentGrn": "`+string(unclaimedAgent)+`", "values": {"model": "haiku"}}
			]`)
			return
		}
		answerJSON(w, http.StatusCreated, `{"grn": "`+string(unclaimedAgent)+`",
			"operator": "grn:acme:default:operator:one", "epoch": 1}`)
	})

	runPoller(t, stub, newRecordingConstructor())

	g.Eventually(func() []string { return claims(stub) }, pollTimeout).Should(ContainElement(string(unclaimedAgent)))
	g.Consistently(func() []string { return claims(stub) }, 10*pollInterval).
		ShouldNot(ContainElement(string(claimedAgent)))
}

func TestPollerKeepsPollingAfterAReadThatFailed(t *testing.T) {
	g := NewWithT(t)
	var reads atomic.Int64
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if reads.Add(1) == 1 {
				answerJSON(w, http.StatusServiceUnavailable, `{"kind": "unavailable", "message": "no"}`)
				return
			}
			answerJSON(w, http.StatusOK, `[{"agentGrn": "`+string(unclaimedAgent)+`", "values": {}}]`)
			return
		}
		answerJSON(w, http.StatusCreated, `{"grn": "`+string(unclaimedAgent)+`",
			"operator": "grn:acme:default:operator:one", "epoch": 1}`)
	})

	runPoller(t, stub, newRecordingConstructor())

	g.Eventually(func() []string { return claims(stub) }, pollTimeout).Should(ContainElement(string(unclaimedAgent)))
}

func TestPollerClaimsTheNextDefinitionAfterAClaimRefusedAsAConflict(t *testing.T) {
	g := NewWithT(t)
	refused := garam.GRN("grn:acme:default:agent:1111111111111111")
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			answerJSON(w, http.StatusOK, `[
				{"agentGrn": "`+string(refused)+`", "values": {}},
				{"agentGrn": "`+string(unclaimedAgent)+`", "values": {}}
			]`)
			return
		}
		if strings.Contains(r.URL.Path, string(refused)) {
			answerJSON(w, http.StatusConflict, `{"kind": "already_exists", "message": "definition already claimed"}`)
			return
		}
		answerJSON(w, http.StatusCreated, `{"grn": "`+string(unclaimedAgent)+`",
			"operator": "grn:acme:default:operator:one", "epoch": 1}`)
	})

	runPoller(t, stub, newRecordingConstructor())

	g.Eventually(func() []string { return claims(stub) }, pollTimeout).Should(ContainElement(string(unclaimedAgent)))
}

func TestPollerStopsWhenItsContextIsCancelled(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerNoDefinitions)

	poller := garam.NewPoller(garam.NewClient(stub.address(), trustedBy(t, stub)),
		newRecordingConstructor(), pollInterval)
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), testr.New(t)))
	stopped := make(chan error, 1)
	go func() { stopped <- poller.Start(ctx) }()

	g.Eventually(func() []stubRequest { return stub.requests() }, pollTimeout).ShouldNot(BeEmpty())
	cancel()
	g.Eventually(stopped, pollTimeout).Should(Receive(BeNil()))
}

// TestPollerConstructsTheAgentsItHoldsAClaimOn says construction is driven by
// every definition this operator holds and not only by the pass that claimed
// one: garam reports a claim for as long as the definition exists, so a poller
// that constructed only on a fresh claim would build nothing after a restart.
func TestPollerConstructsTheAgentsItHoldsAClaimOn(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(`[
		{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 1}},
		{"agentGrn": "`+string(unclaimedAgent)+`", "values": {}}
	]`, ""))
	constructor := newRecordingConstructor()

	runPoller(t, stub, constructor)

	g.Eventually(func() map[garam.GRN]garam.AgentCredential { return constructor.credentials() }, pollTimeout).
		Should(HaveLen(2))
	g.Expect(constructor.credentials()[claimedAgent]).To(Equal(garam.AgentCredential{
		CertificatePEM: []byte("a certificate"),
		KeyPEM:         []byte("a key"),
		IssuerPEM:      []byte("an issuer"),
		ServerRootPEM:  []byte("a root"),
		NotAfter:       time.Date(2026, 8, 26, 15, 1, 43, 0, time.UTC),
	}))
}

// TestPollerCarriesTheKeysItReadsToConstructionAndLeavesTheRestBehind is the
// trust boundary this operator's closed key set buys. garam's values are a
// third party's map, and what reaches construction is the selection parsed out
// of it — so a key naming an image cannot arrive at the one writer of an agent's
// image, whatever a console user types into a definition.
func TestPollerCarriesTheKeysItReadsToConstructionAndLeavesTheRestBehind(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(`[
		{"agentGrn": "`+string(claimedAgent)+`", "values": {
			"tools.pins.message_send": "sha256:aa",
			"image": "example.com/somebody-elses:latest"
		}, "claim": {"epoch": 1}}
	]`, ""))
	constructor := newRecordingConstructor()

	runPoller(t, stub, constructor)

	g.Eventually(func() map[garam.GRN]garam.ToolSet { return constructor.toolSets() }, pollTimeout).
		Should(HaveKey(claimedAgent))
	g.Expect(constructor.toolSets()[claimedAgent]).To(Equal(garam.ToolSet{
		Pins: map[string]string{"message_send": "sha256:aa"},
	}))
}

// TestPollerAsksForNoCertificateOnceTheCredentialIsPlaced is what keeps garam
// from minting a private key every pass. garam generates one per certificate
// and stores none, so a certificate asked for with nowhere to put it is key
// material created and dropped.
func TestPollerAsksForNoCertificateOnceTheCredentialIsPlaced(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(
		`[{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 1}}]`, ""))
	constructor := newRecordingConstructor()

	runPoller(t, stub, constructor)

	g.Eventually(func() []string { return certificatesAsked(stub) }, pollTimeout).
		Should(ConsistOf(string(claimedAgent)))
	g.Consistently(func() []string { return certificatesAsked(stub) }, 10*pollInterval).
		Should(ConsistOf(string(claimedAgent)))
}

// TestPollerAsksAgainForACredentialItCouldNotStore is the write-before-obtained
// rule. A credential that reached no store is gone: garam generates the private
// key per certificate, keeps none, and has already moved on. So a failed write
// is answered by asking for another certificate and never by holding the one in
// hand across passes — a credential kept in this process to be retried is one
// only this process has, and it goes with the process.
func TestPollerAsksAgainForACredentialItCouldNotStore(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(
		`[{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 1}}]`, ""))
	constructor := newRecordingConstructor()
	constructor.refusePlacement = errors.New("the API server refused the write")

	runPoller(t, stub, constructor)

	g.Eventually(func() int { return len(certificatesAsked(stub)) }, pollTimeout).
		Should(BeNumerically(">=", 3))
	g.Expect(constructor.credentials()).To(BeEmpty())
}

// TestPollerConstructsNothingForAnAgentGaramRefuses says a 403 stops
// construction and stops nothing else. garam answers the same for an agent held
// by another operator, for one this operator was replaced on, and for one that
// does not exist, so there is nothing to tell apart and the next read of the
// definitions is the whole of the response.
func TestPollerConstructsNothingForAnAgentGaramRefuses(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(`[
		{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 2}},
		{"agentGrn": "`+string(unclaimedAgent)+`", "values": {}, "claim": {"epoch": 1}}
	]`, claimedAgent))
	constructor := newRecordingConstructor()

	runPoller(t, stub, constructor)

	// The agent garam answers for, so that the absence below is the refusal and
	// not a poller that constructed nothing at all.
	g.Eventually(func() map[garam.GRN]garam.AgentCredential { return constructor.credentials() }, pollTimeout).
		Should(HaveKey(unclaimedAgent))
	g.Expect(constructor.credentials()).NotTo(HaveKey(claimedAgent))
}

// TestPollerCorrectsTheImageOfAnAgentItAlreadyBuilt is the repair a corrected
// --agent-image could not make. The agent it reaches is one whose credential is
// placed, which is where the poller stops asking garam for anything, so a
// correction gated on construction would never run for the agents that need it.
func TestPollerCorrectsTheImageOfAnAgentItAlreadyBuilt(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, answerDefinitionsAndCertificates(
		`[{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 1}}]`, ""))
	constructor := newRecordingConstructor()
	constructor.placed[claimedAgent] = garam.AgentCredential{}
	constructor.stale[claimedAgent] = true

	runPoller(t, stub, constructor)

	g.Eventually(func() []garam.GRN { return constructor.stillStale() }, pollTimeout).Should(BeEmpty())
	// Nothing was asked of garam for this agent, so the correction is the poller
	// reaching an agent it already built and not a second construction.
	g.Expect(certificatesAsked(stub)).To(BeEmpty())
}

// TestPollerConstructsAtTheEpochGaramAnswered carries the value a report to
// garam is fenced by. The epoch is met on the construct path and nowhere else,
// and it reaches that path from two sources — the claim this pass made, and the
// claim a previous process left standing — so the test holds both, at values
// that cannot be confused for each other.
func TestPollerConstructsAtTheEpochGaramAnswered(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			answerJSON(w, http.StatusOK, `[
				{"agentGrn": "`+string(claimedAgent)+`", "values": {}, "claim": {"epoch": 9}},
				{"agentGrn": "`+string(unclaimedAgent)+`", "values": {}}
			]`)
		case strings.HasSuffix(r.URL.Path, "/certificate"):
			answerJSON(w, http.StatusCreated, `{
				"grn": "x", "certificatePem": "a certificate", "privateKeyPem": "a key",
				"issuerPem": "an issuer", "serverRootPem": "a root", "notAfter": "2026-08-26T15:01:43Z"
			}`)
		default:
			answerJSON(w, http.StatusCreated, `{"grn": "`+string(unclaimedAgent)+`",
				"operator": "grn:acme:default:operator:one", "epoch": 4}`)
		}
	})
	constructor := newRecordingConstructor()

	runPoller(t, stub, constructor)

	g.Eventually(func() map[garam.GRN]int64 { return constructor.constructedAt() }, pollTimeout).
		Should(Equal(map[garam.GRN]int64{claimedAgent: 9, unclaimedAgent: 4}))
}
