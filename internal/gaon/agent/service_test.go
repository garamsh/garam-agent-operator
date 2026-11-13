package agent_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garamsh/gaon/lib/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
	"github.com/garamsh/garam-agent-operator/tests/gaon/gaonfake"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

// stubFeed is a stand-in for the control service's controller API.
type stubFeed struct {
	mu       sync.Mutex
	agents   []agent.Desired
	err      error
	rendered []string
}

func (f *stubFeed) Desired(context.Context) ([]agent.Desired, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Desired(nil), f.agents...), f.err
}

func (f *stubFeed) ReportRendered(_ context.Context, g grn.GRN, revision string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rendered = append(f.rendered, g.String()+"@"+revision)
	return nil
}

func (f *stubFeed) set(agents ...agent.Desired) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = agents
}

// stubGaram is a stand-in for garam's machine listener. Each token it
// mints is random, so a test can look for it where it must not be.
type stubGaram struct {
	mu        sync.Mutex
	minted    []agent.Credentials
	mintErr   error
	reports   []string
	reportErr error
}

func (g *stubGaram) MintTokens(_ context.Context, a grn.GRN, _ int64) (agent.Credentials, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mintErr != nil {
		return nil, g.mintErr
	}
	c := agent.Credentials{"agent-token": []byte("garam_at_" + rand.Text()), "control-token": []byte("garam_ct_" + rand.Text())}
	g.minted = append(g.minted, c)
	return c, nil
}

func (g *stubGaram) ReportState(_ context.Context, a grn.GRN, epoch int64, s agent.State) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reports = append(g.reports, fmt.Sprintf("%s@%d=%s", a, epoch, s))
	return g.reportErr
}

func (g *stubGaram) reported() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.reports...)
}

// failingList makes the list of microvms fail, as an unreachable gaon
// does.
type failingList struct{ agent.Cluster }

func (failingList) ListMicroVMs(context.Context) (*api.MicroVMList, error) {
	return nil, errors.New("gaon unreachable")
}

// stubPlacements is a stand-in for the control service's placement
// route. It answers created for the first body it sees from an agent
// and not created for an identical one, as the contract does, unless a
// test scripts an error for the agent.
type stubPlacements struct {
	mu     sync.Mutex
	calls  []placementCall
	errs   map[string]error
	stored map[string]agent.Placement
}

type placementCall struct {
	agent     string
	placement agent.Placement
}

func (p *stubPlacements) RegisterPlacement(_ context.Context, g grn.GRN, body agent.Placement) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, placementCall{g.String(), body})
	if err := p.errs[g.String()]; err != nil {
		return false, err
	}
	if p.stored == nil {
		p.stored = map[string]agent.Placement{}
	}
	if prior, ok := p.stored[g.String()]; ok && prior == body {
		return false, nil
	}
	p.stored[g.String()] = body
	return true, nil
}

func (p *stubPlacements) fail(g grn.GRN, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errs == nil {
		p.errs = map[string]error{}
	}
	p.errs[g.String()] = err
}

func (p *stubPlacements) seen() []placementCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]placementCall(nil), p.calls...)
}

// stubKeys lists the model-key Secrets the deployment created. Each
// carries data, as gaon returns it, so a test can see it go nowhere.
type stubKeys struct {
	items []api.Secret
	value string
}

func (k *stubKeys) ListSecrets(context.Context) (*api.SecretList, error) {
	items := make([]api.Secret, len(k.items))
	copy(items, k.items)
	return &api.SecretList{Items: items}, nil
}

// add creates a Secret of the name and labels, holding a generated key.
func (k *stubKeys) add(name string, labels map[string]string) {
	k.items = append(k.items, api.Secret{
		Metadata: api.ObjectMeta{Name: name, Labels: labels},
		Data:     map[string][]byte{"api-key": []byte(k.value)},
	})
}

type harness struct {
	keys       *stubKeys
	feed       *stubFeed
	garam      *stubGaram
	placements *stubPlacements
	gaon       *gaonfake.Server
	cluster    agent.Cluster
	opts       agent.Options
	svc        agent.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gaon, client := gaonfake.New(t, agent.Selector())
	h := &harness{
		keys: &stubKeys{value: "key-" + randomHex(8)},
		feed: &stubFeed{}, garam: &stubGaram{}, placements: &stubPlacements{}, gaon: gaon, cluster: client,
		opts: agent.Options{
			Image:       "registry.example/garam/agent:1",
			GaramRoot:   []byte("garam root"),
			ControlRoot: []byte("control root"),
			MachineURL:  "https://garam.guest.example",
			ControlURL:  "https://control.guest.example",
			Logger:      slog.New(slog.DiscardHandler),
		},
	}
	h.opts.ModelKeys = h.keys
	h.restart()
	return h
}

func (h *harness) restart() {
	h.svc = agent.NewService(h.feed, h.garam, h.placements, h.cluster, h.opts)
}

// modelKey creates the model-key Secret of d's organization.
func (h *harness) modelKey(d agent.Desired, names ...string) {
	for _, n := range names {
		h.keys.add(n, agent.ModelKeyLabels(d.GRN))
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func desired(id string) agent.Desired {
	return agent.Desired{
		GRN:              grn.New("org1", "agent", id),
		Epoch:            3,
		Revision:         "2",
		MemoryStorage:    "1Gi",
		WorkspaceStorage: "10Gi",
	}
}

func objectName(t *testing.T, d agent.Desired) string {
	t.Helper()
	require.NoError(t, d.GRN.Validate(), "the test's GRN is not one")
	return "agent-" + sha16(d.GRN.String())
}

func TestReconcile_AgentYieldsSecretVolumesAndMicroVM(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	name := objectName(t, d)
	m, ok := h.gaon.MicroVM(t, name)
	require.True(t, ok, "no microvm named %s", name)
	secret, ok := h.gaon.Secret(t, name)
	require.True(t, ok)
	_, memory := h.gaon.Volume(t, name+"-memory")
	_, workspace := h.gaon.Volume(t, name+"-workspace")
	assert.True(t, memory && workspace, "both volumes are written")

	for _, labels := range []map[string]string{m.Metadata.Labels, secret.Metadata.Labels} {
		assert.Equal(t, "agent", labels["garam.sh/role"])
		assert.NotEmpty(t, labels["garam.sh/organization"])
		assert.NotEmpty(t, labels["garam.sh/agent"])
		for k, v := range labels {
			assert.LessOrEqual(t, len(v), 63, "label %s", k)
			assert.NotContains(t, v, ":", "label %s", k)
		}
	}
	assert.Equal(t, d.GRN.String(), m.Metadata.Annotations["garam.sh/agent-grn"])
	assert.Equal(t, "3", m.Metadata.Annotations["garam.sh/epoch"])
	assert.Equal(t, "2", m.Metadata.Annotations["garam.sh/revision"])
	assert.Equal(t, api.RunAlways, m.Spec.RunStrategy)
	assert.Equal(t, "0:0", m.Spec.User, "the supervisor is root and drops each program")

	require.NotEmpty(t, m.Spec.Files)
	for _, f := range m.Spec.Files {
		require.NotNil(t, f.ValueFrom, "file %s is not sourced from the Secret", f.Path)
		assert.Equal(t, name, f.ValueFrom.SecretKeyRef.Name)
		assert.Contains(t, secret.Data, f.ValueFrom.SecretKeyRef.Key)
		assert.Empty(t, f.Content, "file %s holds inline content", f.Path)
		require.NotNil(t, f.Mode)
		assert.Equal(t, int32(0o400), *f.Mode)
		require.NotNil(t, f.Owner)
		assert.Equal(t, int64(65532), f.Owner.UID)
	}

	// The control: the Secret does hold the tokens, so their absence
	// from the spec is the operator's doing and not an empty Secret's.
	require.Len(t, h.garam.minted, 1)
	spec, err := json.Marshal(m)
	require.NoError(t, err)
	for key, token := range h.garam.minted[0] {
		assert.Equal(t, token, secret.Data[key], "the Secret holds %s", key)
		assert.NotContains(t, string(spec), string(token), "the spec holds %s", key)
	}
	assert.NotContains(t, string(spec), "garam root")
}

func TestReconcile_ReportsOnlyWhatItObserved(t *testing.T) {
	ready := []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}
	exit := &api.ExitRecord{Code: 1, Time: time.Unix(1, 0)}
	tests := []struct {
		name   string
		status api.MicroVMStatus
		want   agent.State
	}{
		{"a pending microvm is provisioned", api.MicroVMStatus{Phase: api.MicroVMPending}, agent.StateProvisioned},
		{"a running and ready microvm is ready", api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: ready}, agent.StateReady},
		{"a running microvm that is not ready is provisioned", api.MicroVMStatus{Phase: api.MicroVMRunning}, agent.StateProvisioned},
		{"a running microvm whose Ready is false is provisioned", api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionFalse}}}, agent.StateProvisioned},
		{"a ready status of another epoch is void and provisioned", api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: ready, BindingEpoch: 9}, agent.StateProvisioned},
		{"a failed microvm with its last exit is failed", api.MicroVMStatus{Phase: api.MicroVMFailed, LastExit: exit}, agent.StateFailed},
		{"a failed microvm without a last exit is provisioned", api.MicroVMStatus{Phase: api.MicroVMFailed}, agent.StateProvisioned},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			d := desired("a1")
			h.feed.set(d)
			require.NoError(t, h.svc.Reconcile(t.Context()))
			h.gaon.SetStatus(t, objectName(t, d), tc.status)
			// A pass over the first state reported provisioned; the
			// second reports what the status now says.
			h.restart()

			require.NoError(t, h.svc.Reconcile(t.Context()))

			reports := h.garam.reported()
			assert.Equal(t, fmt.Sprintf("%s@3=%s", d.GRN, tc.want), reports[len(reports)-1])
		})
	}
}

func TestReconcile_ReportsEachChangeOnce(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Equal(t, []string{d.GRN.String() + "@3=provisioned"}, h.garam.reported())
	assert.Equal(t, []string{d.GRN.String() + "@2"}, h.feed.rendered)

	h.gaon.SetStatus(t, objectName(t, d), api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}})
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.garam.reported(), 2)
}

func TestReconcile_ReportsNothingWhereNothingWasRead(t *testing.T) {
	h := newHarness(t)
	h.feed.set(desired("a1"))
	svc := agent.NewService(h.feed, h.garam, h.placements, failingList{h.cluster}, h.opts)

	err := svc.Reconcile(t.Context())

	require.Error(t, err)
	assert.Empty(t, h.garam.reported())
	assert.Empty(t, h.garam.minted, "no token is minted for an agent whose objects were not read")
	assert.Zero(t, h.gaon.Count("PUT", "microvms")+h.gaon.Count("PUT", "secrets"))
}

func TestReconcile_ADroppedReportOfAStaleEpochDoesNotFailThePass(t *testing.T) {
	h := newHarness(t)
	h.feed.set(desired("a1"))
	h.garam.reportErr = agent.ErrStaleEpoch

	require.NoError(t, h.svc.Reconcile(t.Context()))
	require.NoError(t, h.svc.Reconcile(t.Context()))

	assert.Len(t, h.garam.reported(), 1, "a refused report is not repeated until something changes")
}

func TestReconcile_AStoppedAgentIsHaltedAndNothingIsDeleted(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	name := objectName(t, d)
	// A state that changed while the agent is stopped is not reported:
	// without this the dedupe would hide a report sent on a stop.
	h.gaon.SetStatus(t, name, api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}})

	d.Stopped = true
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, ok := h.gaon.MicroVM(t, name)
	require.True(t, ok)
	assert.Equal(t, api.RunHalted, m.Spec.RunStrategy)
	assert.Zero(t, h.gaon.Count("DELETE", "microvms")+h.gaon.Count("DELETE", "secrets")+h.gaon.Count("DELETE", "volumes"))
	_, secret := h.gaon.Secret(t, name)
	_, volume := h.gaon.Volume(t, name+"-workspace")
	assert.True(t, secret && volume, "the Secret and the Volumes stay")
	assert.Len(t, h.garam.reported(), 1, "a halt reports no state")

	// The control: the stop is the feed's, and ending it starts the
	// agent again, with nothing minted a second time.
	d.Stopped = false
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	m, _ = h.gaon.MicroVM(t, name)
	assert.Equal(t, api.RunAlways, m.Spec.RunStrategy)
	assert.Len(t, h.garam.minted, 1)
}

func TestReconcile_AStoppedAgentWithNoMicroVMIsNotStarted(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	d.Stopped = true
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	assert.Zero(t, h.gaon.Count("PUT", "microvms")+h.gaon.Count("PUT", "secrets")+h.gaon.Count("PUT", "volumes"))
	assert.Empty(t, h.garam.minted)
}

func TestReconcile_AMissingVolumeOfAnExistingMicroVMIsNotMadeAgain(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	name := objectName(t, d)
	h.gaon.SetStatus(t, name, api.MicroVMStatus{Phase: api.MicroVMRunning, Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}})
	h.gaon.RemoveVolume(t, name+"-memory")
	before, _ := h.gaon.MicroVM(t, name)
	puts := h.gaon.Count("PUT", "volumes") + h.gaon.Count("PUT", "microvms") + h.gaon.Count("PUT", "secrets")
	reports := len(h.garam.reported())
	h.restart()

	err := h.svc.Reconcile(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), name+"-memory")
	assert.Equal(t, puts, h.gaon.Count("PUT", "volumes")+h.gaon.Count("PUT", "microvms")+h.gaon.Count("PUT", "secrets"), "nothing is written")
	_, remade := h.gaon.Volume(t, name+"-memory")
	assert.False(t, remade)
	after, _ := h.gaon.MicroVM(t, name)
	assert.Equal(t, before, after, "the microvm is left as it was")
	assert.Len(t, h.garam.reported(), reports, "no ready is sent")
}

func TestReconcile_AnAgentThatLeftTheFeedIsHaltedAndKept(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))

	h.feed.set()
	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, ok := h.gaon.MicroVM(t, objectName(t, d))
	require.True(t, ok)
	assert.Equal(t, api.RunHalted, m.Spec.RunStrategy)
	assert.Zero(t, h.gaon.Count("DELETE", "microvms"))
	_, volume := h.gaon.Volume(t, objectName(t, d)+"-memory")
	assert.True(t, volume)
}

func TestReconcile_ARestartedOperatorCreatesNothingTwice(t *testing.T) {
	h := newHarness(t)
	h.feed.set(desired("a1"), desired("a2"))
	require.NoError(t, h.svc.Reconcile(t.Context()))
	writes := h.gaon.Count("PUT", "microvms") + h.gaon.Count("PUT", "secrets") + h.gaon.Count("PUT", "volumes")
	require.Equal(t, 8, writes, "two agents write four objects each")
	require.Len(t, h.garam.minted, 2)

	h.restart()
	require.NoError(t, h.svc.Reconcile(t.Context()))

	assert.Equal(t, writes, h.gaon.Count("PUT", "microvms")+h.gaon.Count("PUT", "secrets")+h.gaon.Count("PUT", "volumes"))
	assert.Len(t, h.garam.minted, 2, "no second token for a Secret that exists")
}

func TestReconcile_AMissingSecretIsMintedAgainAndAnExistingOneIsNot(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	h.garam.mintErr = errors.New("garam unavailable")

	require.Error(t, h.svc.Reconcile(t.Context()))
	_, microvm := h.gaon.MicroVM(t, objectName(t, d))
	assert.False(t, microvm, "no microvm is made without its credential")
	assert.Empty(t, h.garam.reported(), "nothing is reported for an agent that was not rendered")

	h.garam.mintErr = nil
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.garam.minted, 1)
}

// collidingGaram mints a credential named like a trust root.
type collidingGaram struct{ stubGaram }

func (g *collidingGaram) MintTokens(context.Context, grn.GRN, int64) (agent.Credentials, error) {
	return agent.Credentials{agent.KeyGaramRoot: []byte("garam_at_x")}, nil
}

func TestReconcile_ACredentialNamedLikeATrustRootIsRefused(t *testing.T) {
	h := newHarness(t)
	h.feed.set(desired("a1"))
	svc := agent.NewService(h.feed, &collidingGaram{}, h.placements, h.cluster, h.opts)

	err := svc.Reconcile(t.Context())

	require.Error(t, err)
	assert.Zero(t, h.gaon.Count("PUT", "secrets")+h.gaon.Count("PUT", "microvms"))
}

func TestReconcile_DoesNotTouchWhatItDoesNotOwn(t *testing.T) {
	// A client built with no selector lists every object, so the filter
	// in the operator is what is under test.
	gaon, everything := gaonfake.New(t, nil)
	other := &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: "someone-else", Labels: map[string]string{"app": "other"}},
		Spec:     api.MicroVMSpec{Image: "x", RunStrategy: api.RunAlways},
	}
	_, err := everything.PutMicroVM(t.Context(), other)
	require.NoError(t, err)
	h := newHarness(t)
	svc := agent.NewService(h.feed, h.garam, h.placements, everything, h.opts)
	h.feed.set(desired("a1"))

	require.NoError(t, svc.Reconcile(t.Context()))

	got, ok := gaon.MicroVM(t, "someone-else")
	require.True(t, ok)
	assert.Equal(t, api.RunAlways, got.Spec.RunStrategy, "an object without the operator's label is not halted")
	owned, ok := gaon.MicroVM(t, objectName(t, desired("a1")))
	require.True(t, ok)
	assert.Equal(t, api.RunAlways, owned.Spec.RunStrategy)
}

func TestCredentials_NeverPrintTheirValues(t *testing.T) {
	c := agent.Credentials{"agent-token": []byte("garam_at_secretvalue")}
	var out strings.Builder
	slog.New(slog.NewTextHandler(&out, nil)).Info("minted", "credentials", c)

	for _, s := range []string{out.String(), fmt.Sprint(c), fmt.Sprintf("%v %+v %#v", c, c, c)} {
		assert.NotContains(t, s, "secretvalue")
	}
	assert.Contains(t, out.String(), "redacted")
}

func sha16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// envOf returns the environment of m by name, and fails the test on a
// name given twice, which gaon refuses.
func envOf(t *testing.T, m api.MicroVM) map[string]api.EnvVar {
	t.Helper()
	env := map[string]api.EnvVar{}
	for _, e := range m.Spec.Env {
		_, dup := env[e.Name]
		require.False(t, dup, "env %s is given twice", e.Name)
		env[e.Name] = e
	}
	return env
}

func TestReconcile_TheMicroVMCarriesTheGuestsCommandAndSettings(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, ok := h.gaon.MicroVM(t, objectName(t, d))
	require.True(t, ok)
	assert.Equal(t, []string{"/usr/local/bin/gaon-guest"}, m.Spec.Command,
		"a command replaces the image's entrypoint, so it names the supervisor")
	assert.Empty(t, m.Spec.Args)
	env := envOf(t, m)
	assert.Equal(t, d.GRN.String(), env["GUEST_AGENT_GRN"].Value)
	assert.Equal(t, "3", env["GUEST_ASSIGNMENT_EPOCH"].Value)
	assert.Equal(t, "https://garam.guest.example", env["GUEST_MACHINE_URL"].Value)
	assert.Equal(t, "https://control.guest.example", env["GUEST_CONTROL_URL"].Value)
	assert.Len(t, env, 4, "with no model key, nothing else is rendered")
	for name, e := range env {
		assert.Nil(t, e.ValueFrom, name)
	}
}

// configured returns an agent with a model, an embedding, tool pins and
// an ego, its keys named by the references given.
func configured(id, modelRef, embeddingRef string) agent.Desired {
	d := desired(id)
	d.Configuration = agent.Configuration{
		Model: &agent.Model{
			Provider: "openai", BaseURL: "https://llm.example/v1", Name: "m-" + id, APIKeyRef: modelRef,
			Embedding: &agent.Embedding{BaseURL: "https://emb.example/v1", Name: "e-" + id, APIKeyRef: embeddingRef},
		},
		Ego:   "You are " + id + ".\n",
		Tools: map[string]string{"web": "sha256:abc"},
	}
	return d
}

func fileAt(t *testing.T, m api.MicroVM, path string) api.MicroVMFile {
	t.Helper()
	for _, f := range m.Spec.Files {
		if f.Path == path {
			return f
		}
	}
	require.Failf(t, "no such file", "%s", path)
	return api.MicroVMFile{}
}

func TestReconcile_TheConfigurationIsFilesAndTheKeysAreEnvSourcedFromSecrets(t *testing.T) {
	h := newHarness(t)
	d := configured("a1", "model-credentials/api-key", "embedding-credentials/emb-key")
	h.modelKey(d, "model-credentials", "embedding-credentials")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, _ := h.gaon.MicroVM(t, objectName(t, d))
	env := envOf(t, m)
	for name, want := range map[string][2]string{
		"MODEL_API_KEY":     {"model-credentials", "api-key"},
		"EMBEDDING_API_KEY": {"embedding-credentials", "emb-key"},
	} {
		e := env[name]
		assert.Empty(t, e.Value, "the key is never an inline value")
		require.NotNil(t, e.ValueFrom, name)
		require.NotNil(t, e.ValueFrom.SecretKeyRef, name)
		assert.Equal(t, want[0], e.ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, want[1], e.ValueFrom.SecretKeyRef.Key)
	}
	assert.Equal(t, "MODEL_API_KEY,EMBEDDING_API_KEY", env["GUEST_RUNTIME_ENV"].Value,
		"the supervisor hands the variables to the runtime alone")
	assert.Equal(t, guest.RuntimeConfigFile, env["GUEST_RUNTIME_CONFIG"].Value)
	assert.Equal(t, guest.RuntimeEgoFile, env["GUEST_RUNTIME_EGO"].Value)
	_, secret := h.gaon.Secret(t, "model-credentials")
	assert.False(t, secret, "the operator creates no model Secret")

	conf := fileAt(t, m, guest.RuntimeConfigFile)
	assert.Nil(t, conf.ValueFrom)
	assert.Equal(t, `tools:
    pins:
        web: sha256:abc
model:
    provider: openai
    base-url: https://llm.example/v1
    model: m-a1
    api-key-env: MODEL_API_KEY
embedding:
    base-url: https://emb.example/v1
    model: e-a1
    api-key-env: EMBEDDING_API_KEY
`, conf.Content)
	ego := fileAt(t, m, guest.RuntimeEgoFile)
	assert.Equal(t, "You are a1.\n", ego.Content)
	for _, f := range []api.MicroVMFile{conf, ego} {
		require.NotNil(t, f.Mode, f.Path)
		assert.Equal(t, int32(0o400), *f.Mode, f.Path)
		require.NotNil(t, f.Owner, f.Path)
		assert.Equal(t, int64(65532), f.Owner.UID, f.Path)
	}
}

func TestReconcile_TwoAgentsWithTwoReferencesRenderApart(t *testing.T) {
	h := newHarness(t)
	a := configured("a1", "creds-one/key-one", "")
	b := configured("a2", "creds-two/key-two", "")
	h.modelKey(a, "creds-one", "creds-two")
	h.feed.set(a, b)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	for _, tc := range []struct {
		d       agent.Desired
		secret  string
		key     string
		foreign string
	}{{a, "creds-one", "key-one", "creds-two"}, {b, "creds-two", "key-two", "creds-one"}} {
		m, _ := h.gaon.MicroVM(t, objectName(t, tc.d))
		ref := envOf(t, m)["MODEL_API_KEY"].ValueFrom.SecretKeyRef
		assert.Equal(t, tc.secret, ref.Name)
		assert.Equal(t, tc.key, ref.Key)
		spec, err := json.Marshal(m)
		require.NoError(t, err)
		assert.NotContains(t, string(spec), tc.foreign, "an agent's spec names its own key alone")
		assert.NotContains(t, envOf(t, m), "EMBEDDING_API_KEY", "an endpoint with no reference takes no key")
	}
}

func TestReconcile_NoSettingNamesAModelKey(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, _ := h.gaon.MicroVM(t, objectName(t, d))
	for name := range envOf(t, m) {
		assert.False(t, strings.HasPrefix(name, "MODEL_KEY"), name)
	}
	assert.NotContains(t, envOf(t, m), "MODEL_API_KEY", "an agent with no model carries no key")
	assert.NotContains(t, envOf(t, m), "GUEST_RUNTIME_CONFIG")
}

func TestReconcile_ABadConfigurationWritesNothingAndFailsThePass(t *testing.T) {
	for name, d := range map[string]agent.Desired{
		"a reference with no key":           configured("a1", "model-credentials", ""),
		"a reference to the agent's Secret": configured("a1", "agent-0123456789abcdef/api-key", ""),
		"a model with no name": func() agent.Desired {
			d := configured("a1", "c/k", "")
			d.Configuration.Model.Name = ""
			return d
		}(),
		"a model other than mock with no embedding": func() agent.Desired {
			d := configured("a1", "c/k", "")
			d.Configuration.Model.Embedding = nil
			return d
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.feed.set(d, desired("a2"))

			require.Error(t, h.svc.Reconcile(t.Context()))

			_, ok := h.gaon.MicroVM(t, objectName(t, desired("a2")))
			assert.True(t, ok, "the other agent is still written")
			_, ok = h.gaon.MicroVM(t, objectName(t, d))
			assert.False(t, ok)
			_, ok = h.gaon.Secret(t, objectName(t, d))
			assert.False(t, ok, "no Secret is minted for an agent that cannot render")
		})
	}
}

func TestReconcile_APlacementIsRegisteredOnceWithTheUidsAndTheTokenDigest(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	name := objectName(t, d)
	m, _ := h.gaon.MicroVM(t, name)
	memory, _ := h.gaon.Volume(t, name+"-memory")
	secret, _ := h.gaon.Secret(t, name)
	sum := sha256.Sum256(secret.Data["placement-token"])
	require.NotEmpty(t, m.Metadata.UID)
	require.NotEmpty(t, memory.Metadata.UID)
	require.NotEqual(t, m.Metadata.UID, memory.Metadata.UID)
	assert.Equal(t, []placementCall{{d.GRN.String(), agent.Placement{
		Epoch: "3", PodUID: string(m.Metadata.UID), PVCUID: string(memory.Metadata.UID), TokenSHA256: hex.EncodeToString(sum[:]),
	}}}, h.placements.seen())

	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.placements.seen(), 1, "a registered body is not presented again by the same process")
}

func TestReconcile_ARestartPresentsTheSameBodyAndTheRepeatIsNotAnError(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))

	h.restart()
	require.NoError(t, h.svc.Reconcile(t.Context()))

	calls := h.placements.seen()
	require.Len(t, calls, 2)
	assert.Equal(t, calls[0], calls[1], "the body is rebuilt from gaon's objects, byte for byte")
}

func TestReconcile_AStoppedAgentIsNotRegistered(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	d.Stopped = true
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Empty(t, h.placements.seen(), "a stopped agent is not created, so it has no placement")

	// The control: the same agent running registers, and stopped
	// afterwards, in a process that remembers nothing, it does not.
	d.Stopped = false
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	require.Len(t, h.placements.seen(), 1)
	d.Stopped = true
	h.feed.set(d)
	h.restart()
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.placements.seen(), 1, "a stopped microvm that exists is not registered either")
}

func TestReconcile_ASupersededPlacementIsNeverPresentedAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"placement_superseded", errorx.Wrap(agent.ErrPlacementSuperseded, "placement_superseded")},
		{"epoch_superseded", errorx.Wrap(agent.ErrPlacementSuperseded, "epoch_superseded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			now := time.Unix(1000, 0)
			h.opts.Now = func() time.Time { return now }
			h.restart()
			d := desired("a1")
			h.feed.set(d)
			h.placements.fail(d.GRN, tc.err)

			require.NoError(t, h.svc.Reconcile(t.Context()), "a refusal is not a failed pass")
			now = now.Add(24 * time.Hour)
			require.NoError(t, h.svc.Reconcile(t.Context()))

			assert.Len(t, h.placements.seen(), 1, "the body is not presented again, however long after")
			assert.Len(t, h.garam.reported(), 1, "the agent is still reported, once")
		})
	}
}

func TestReconcile_ASupersededBodyIsReplacedByANewOne(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	h.placements.fail(d.GRN, errorx.Wrap(agent.ErrPlacementSuperseded, "superseded"))
	require.NoError(t, h.svc.Reconcile(t.Context()))

	next := d
	next.Epoch = 4
	h.feed.set(next)
	h.placements.fail(d.GRN, nil)
	require.NoError(t, h.svc.Reconcile(t.Context()))

	calls := h.placements.seen()
	require.Len(t, calls, 2)
	assert.Equal(t, "4", calls[1].placement.Epoch, "a different body is a new attempt")
}

func TestReconcile_ARefusedPlacementIsPresentedAgainAfterFiveMinutes(t *testing.T) {
	h := newHarness(t)
	now := time.Unix(1000, 0)
	h.opts.Now = func() time.Time { return now }
	h.restart()
	d := desired("a1")
	h.feed.set(d)
	h.placements.fail(d.GRN, errorx.Wrap(agent.ErrPlacementRefused, "previous_mismatch"))

	require.NoError(t, h.svc.Reconcile(t.Context()))
	now = now.Add(5*time.Minute - time.Second)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.placements.seen(), 1, "not before five minutes")

	now = now.Add(time.Second)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.placements.seen(), 2, "at five minutes")
}

func TestReconcile_AnUnreachableControlServiceFailsThePassAndIsRetried(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	h.placements.fail(d.GRN, errors.New("control unreachable"))

	require.Error(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.garam.reported(), 1, "the report is still made")

	h.placements.fail(d.GRN, nil)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	assert.Len(t, h.placements.seen(), 2)
}

func TestReconcile_ThePlacementTokenIsMintedOnceIntoTheSecret(t *testing.T) {
	h := newHarness(t)
	a, b := desired("a1"), desired("a2")
	h.feed.set(a, b)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	secretA, _ := h.gaon.Secret(t, objectName(t, a))
	secretB, _ := h.gaon.Secret(t, objectName(t, b))
	tokenA := secretA.Data["placement-token"]
	raw, err := hex.DecodeString(string(tokenA))
	require.NoError(t, err, "the token is hex")
	assert.Len(t, raw, 32)
	assert.NotEqual(t, tokenA, secretB.Data["placement-token"], "each agent has its own")

	m, _ := h.gaon.MicroVM(t, objectName(t, a))
	var delivered bool
	for _, f := range m.Spec.Files {
		if f.Path == guest.CredentialsDir+"placement-token" {
			delivered = true
			require.NotNil(t, f.ValueFrom)
			assert.Equal(t, "placement-token", f.ValueFrom.SecretKeyRef.Key)
			assert.Empty(t, f.Content)
		}
	}
	assert.True(t, delivered, "the token is a file sourced from the Secret")
	spec, err := json.Marshal(m)
	require.NoError(t, err)
	assert.NotContains(t, string(spec), string(tokenA))

	h.restart()
	require.NoError(t, h.svc.Reconcile(t.Context()))
	again, _ := h.gaon.Secret(t, objectName(t, a))
	assert.Equal(t, tokenA, again.Data["placement-token"], "an existing Secret keeps its token")
}

func TestReconcile_AnExistingSecretIsNotGivenAPlacementToken(t *testing.T) {
	h := newHarness(t)
	d := desired("a1")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	secret, _ := h.gaon.Secret(t, objectName(t, d))
	delete(secret.Data, "placement-token")
	_, err := h.cluster.PutSecret(t.Context(), &secret)
	require.NoError(t, err)

	h.restart()
	require.Error(t, h.svc.Reconcile(t.Context()), "no placement can be registered without the token")

	after, _ := h.gaon.Secret(t, objectName(t, d))
	assert.NotContains(t, after.Data, "placement-token",
		"a Secret that exists is never minted over, whatever it lacks")
}

// collidingPlacementGaram mints a credential named like the placement
// token.
type collidingPlacementGaram struct{ stubGaram }

func (g *collidingPlacementGaram) MintTokens(context.Context, grn.GRN, int64) (agent.Credentials, error) {
	return agent.Credentials{agent.KeyPlacementToken: []byte("garam_at_x")}, nil
}

func TestReconcile_ACredentialNamedLikeThePlacementTokenIsRefused(t *testing.T) {
	h := newHarness(t)
	h.feed.set(desired("a1"))
	svc := agent.NewService(h.feed, &collidingPlacementGaram{}, h.placements, h.cluster, h.opts)

	require.Error(t, svc.Reconcile(t.Context()))
	assert.Zero(t, h.gaon.Count("PUT", "secrets")+h.gaon.Count("PUT", "microvms"))
}

func TestRender_IsTheMicroVMAReconcileWrites(t *testing.T) {
	h := newHarness(t)
	d := configured("a1", "model-credentials/api-key", "")
	h.modelKey(d, "model-credentials")
	h.feed.set(d)
	require.NoError(t, h.svc.Reconcile(t.Context()))
	written, _ := h.gaon.MicroVM(t, objectName(t, d))

	_, rendered, err := agent.Render(h.opts, d, h.garam.minted[0], h.keys.items)

	require.NoError(t, err)
	assert.Equal(t, written.Spec, rendered.Spec,
		"a run that starts from Render starts from what the operator writes")
}

// A model key reference is honoured for a Secret that carries the model
// key role and the agent's organization, and for no other: gaon has no
// namespaces, so any other would put a stranger's value in this agent's
// environment.
func TestReconcile_AKeyReferenceNamesOnlyALabelledSecretOfTheAgentsOrganization(t *testing.T) {
	other := grn.New("org2", "agent", "x")
	for name, setup := range map[string]func(h *harness){
		"missing": func(h *harness) {},
		"unlabelled": func(h *harness) {
			h.keys.add("model-credentials", nil)
		},
		"labelled with the role alone": func(h *harness) {
			h.keys.add("model-credentials", map[string]string{"garam.sh/role": "model-key"})
		},
		"labelled for another organization": func(h *harness) {
			h.modelKey(agent.Desired{GRN: other}, "model-credentials")
		},
		"labelled for the organization with another role": func(h *harness) {
			l := agent.ModelKeyLabels(desired("a1").GRN)
			l["garam.sh/role"] = "agent"
			h.keys.add("model-credentials", l)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setup(h)
			d := configured("a1", "model-credentials/api-key", "")
			h.feed.set(d, desired("a2"))

			err := h.svc.Reconcile(t.Context())

			require.Error(t, err)
			assert.Contains(t, err.Error(), "model-credentials/api-key", "the error names the reference")
			assert.NotContains(t, err.Error(), h.keys.value)
			_, ok := h.gaon.MicroVM(t, objectName(t, d))
			assert.False(t, ok, "nothing starts for the agent")
			_, ok = h.gaon.Secret(t, objectName(t, d))
			assert.False(t, ok, "no Secret is minted for it")
			_, ok = h.gaon.MicroVM(t, objectName(t, desired("a2")))
			assert.True(t, ok, "the other agent is still written")
		})
	}
}

func TestReconcile_WithoutAModelKeyListerNoReferenceIsHonoured(t *testing.T) {
	h := newHarness(t)
	d := configured("a1", "model-credentials/api-key", "")
	h.modelKey(d, "model-credentials")
	h.opts.ModelKeys = nil
	h.restart()
	h.feed.set(d)

	require.Error(t, h.svc.Reconcile(t.Context()))

	_, ok := h.gaon.MicroVM(t, objectName(t, d))
	assert.False(t, ok)
}

func TestReconcile_TwoOrganizationsWithOneKeyNameEachGetTheirOwn(t *testing.T) {
	h := newHarness(t)
	a := configured("a1", "shared-name/api-key", "")
	b := configured("a2", "shared-name/api-key", "")
	b.GRN = grn.New("org2", "agent", "a2")
	h.modelKey(a, "shared-name")
	h.feed.set(a, b)

	require.Error(t, h.svc.Reconcile(t.Context()), "org2 has no Secret of that name yet")
	_, ok := h.gaon.MicroVM(t, objectName(t, a))
	assert.True(t, ok, "org1's agent renders from org1's Secret")
	_, ok = h.gaon.MicroVM(t, objectName(t, b))
	assert.False(t, ok, "org2's agent does not take org1's Secret of the same name")

	h.modelKey(b, "shared-name")
	require.NoError(t, h.svc.Reconcile(t.Context()))
	for _, d := range []agent.Desired{a, b} {
		m, _ := h.gaon.MicroVM(t, objectName(t, d))
		ref := envOf(t, m)["MODEL_API_KEY"].ValueFrom.SecretKeyRef
		assert.Equal(t, "shared-name", ref.Name)
	}
}

func TestReconcile_NoKeyValueReachesASpec(t *testing.T) {
	h := newHarness(t)
	d := configured("a1", "model-credentials/api-key", "")
	h.modelKey(d, "model-credentials")
	h.feed.set(d)

	require.NoError(t, h.svc.Reconcile(t.Context()))

	m, _ := h.gaon.MicroVM(t, objectName(t, d))
	spec, err := json.Marshal(m)
	require.NoError(t, err)
	assert.NotContains(t, string(spec), h.keys.value, "the listed data is never rendered")
}

// The label is documented to a deployer as the first 32 hex digits of
// the SHA-256 of "grn:root/iam/org/<id>"; this pins the document's
// worked example to the code.
func TestModelKeyLabels_TheOrganizationDigestIsTheDocumentedExample(t *testing.T) {
	labels := agent.ModelKeyLabels(grn.New("org1", "agent", "a1"))

	assert.Equal(t, "37a401b54d687a3e792937666c548e11", labels["garam.sh/organization"])
	assert.Equal(t, "model-key", labels["garam.sh/role"])
	sum := sha256.Sum256([]byte("grn:root/iam/org/org1"))
	assert.Equal(t, hex.EncodeToString(sum[:])[:32], labels["garam.sh/organization"])
}
