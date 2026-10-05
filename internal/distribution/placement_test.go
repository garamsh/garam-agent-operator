package distribution_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// digest is a SHA-256 as the manager writes one, of what.
func digest(what string) string {
	sum := sha256.Sum256([]byte(what))
	return hex.EncodeToString(sum[:])
}

// placementWire is a placement registration's body as the manager sends it, and the answer's
// fields where Agent is set.
type placementWire struct {
	Agent       string        `json:"agent,omitempty"`
	Epoch       string        `json:"epoch"`
	PodUID      string        `json:"podUid"`
	PVCUID      string        `json:"pvcUid"`
	TokenSHA256 string        `json:"tokenSha256"`
	Previous    *previousWire `json:"previous,omitempty"`
}

type previousWire struct {
	PodUID              string `json:"podUid"`
	WriterStoppedSHA256 string `json:"writerStoppedSha256,omitempty"`
}

// wireOf decodes a body or an answer.
func wireOf(t *testing.T, raw []byte) placementWire {
	t.Helper()
	var w placementWire
	require.NoError(t, json.Unmarshal(raw, &w), string(raw))
	return w
}

// marshal is v as JSON.
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// placementBody is a registration of pod, replacing previous where it names one, with the
// digest of previous's evidence where evidence is not empty.
func placementBody(pod, previous, evidence string) string {
	in := placementWire{Epoch: epoch, PodUID: pod, PVCUID: "pvc-1", TokenSHA256: digest("token of " + pod)}
	if previous != "" {
		in.Previous = &previousWire{PodUID: previous}
		if evidence != "" {
			in.Previous.WriterStoppedSHA256 = digest(evidence)
		}
	}
	return marshal(in)
}

// registerPlacement posts body for agent with client, and returns the status, the raw answer and
// its decoded fields.
func (e *env) registerPlacement(t *testing.T, client *http.Client, agent, body string) (int, []byte, map[string]string) {
	t.Helper()
	resp, err := client.Post(e.server.URL+"/v1/operators/self/agents/"+agent+"/placements",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, raw, out
}

// mustRegister registers body for agentA and requires status.
func (e *env) mustRegister(t *testing.T, body string, status int) []byte {
	t.Helper()
	got, raw, _ := e.registerPlacement(t, e.withCert, agentA, body)
	require.Equal(t, status, got, string(raw))
	return raw
}

func TestRegisterPlacement_StoresTheFirstPlacementAndAnswersARepeat(t *testing.T) {
	e := newEnv(t)
	// The wire's own form of a first placement: previous is null.
	body := strings.Replace(placementBody("pod-1", "", ""), `}`, `,"previous":null}`, 1)

	status, first, _ := e.registerPlacement(t, e.withCert, agentA, body)
	require.Equal(t, http.StatusCreated, status, string(first))
	assert.Equal(t, placementWire{
		Agent: agentA, Epoch: epoch, PodUID: "pod-1", PVCUID: "pvc-1", TokenSHA256: digest("token of pod-1"),
	}, wireOf(t, first))

	// The manager's retry after a lost answer is answered the same.
	status, repeat, _ := e.registerPlacement(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusOK, status, string(repeat))
	assert.Equal(t, first, repeat)
}

func TestRegisterPlacement_SupersededPlacementRefused(t *testing.T) {
	e := newEnv(t)
	first := placementBody("pod-1", "", "")
	e.mustRegister(t, first, http.StatusCreated)
	second := placementBody("pod-2", "pod-1", "evidence of pod-1")
	e.mustRegister(t, second, http.StatusCreated)

	// A replay of the replaced registration, and a refresh of its leaf under another leaf.
	other, _ := clientWithLeaf(t, e.server, controller)
	for _, client := range []*http.Client{e.withCert, other} {
		status, raw, out := e.registerPlacement(t, client, agentA, first)
		assert.Equal(t, http.StatusConflict, status, string(raw))
		assert.Equal(t, "placement_superseded", out["kind"])
	}

	// Control: the placement that replaced it is still the one held.
	e.mustRegister(t, second, http.StatusOK)
	e.mustRegister(t, placementBody("pod-3", "pod-2", "evidence of pod-2"), http.StatusCreated)
}

func TestRegisterPlacement_PreviousOtherThanTheHeldOneRefused(t *testing.T) {
	tests := []struct {
		name  string
		setup []string
		body  string
	}{
		{"a first placement naming a previous one", nil, placementBody("pod-1", "pod-0", "evidence of pod-0")},
		{"a placement naming one never held", []string{placementBody("pod-1", "", "")},
			placementBody("pod-2", "pod-9", "evidence of pod-9")},
		{"a placement naming none while one is held", []string{placementBody("pod-1", "", "")},
			placementBody("pod-2", "", "")},
		{"a placement naming a superseded one", []string{
			placementBody("pod-1", "", ""), placementBody("pod-2", "pod-1", "evidence of pod-1"),
		}, placementBody("pod-3", "pod-1", "evidence of pod-1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			for _, body := range tt.setup {
				e.mustRegister(t, body, http.StatusCreated)
			}
			status, raw, out := e.registerPlacement(t, e.withCert, agentA, tt.body)
			assert.Equal(t, http.StatusConflict, status, string(raw))
			assert.Equal(t, "previous_mismatch", out["kind"])

			// Control: the same Pod naming the placement held, if any, is registered.
			held := ""
			if len(tt.setup) > 0 {
				held = wireOf(t, e.mustRegister(t, tt.setup[len(tt.setup)-1], http.StatusOK)).PodUID
			}
			e.mustRegister(t, placementBody(wireOf(t, []byte(tt.body)).PodUID, held, "evidence of "+held), http.StatusCreated)
		})
	}
}

func TestRegisterPlacement_ReplacementWithoutEvidenceRefused(t *testing.T) {
	e := newEnv(t)
	e.mustRegister(t, placementBody("pod-1", "", ""), http.StatusCreated)

	status, raw, out := e.registerPlacement(t, e.withCert, agentA, placementBody("pod-2", "pod-1", ""))
	assert.Equal(t, http.StatusConflict, status, string(raw))
	assert.Equal(t, "evidence_missing", out["kind"])

	// Control: the same replacement carrying the digest of the evidence is registered.
	e.mustRegister(t, placementBody("pod-2", "pod-1", "evidence of pod-1"), http.StatusCreated)
}

func TestRegisterPlacement_HeldPlacementRegisteredDifferentlyRefused(t *testing.T) {
	e := newEnv(t)
	body := placementBody("pod-1", "", "")
	e.mustRegister(t, body, http.StatusCreated)

	for _, change := range []func(*placementWire){
		func(in *placementWire) { in.PVCUID = "pvc-other" },
		func(in *placementWire) { in.TokenSHA256 = digest("another token") },
		func(in *placementWire) {
			in.Previous = &previousWire{PodUID: "pod-0", WriterStoppedSHA256: digest("x")}
		},
	} {
		changed := wireOf(t, []byte(body))
		change(&changed)
		status, raw, out := e.registerPlacement(t, e.withCert, agentA, marshal(changed))
		assert.Equal(t, http.StatusConflict, status, string(raw))
		assert.Equal(t, "placement_conflict", out["kind"])
	}

	// Control: the registration as it was made is answered.
	e.mustRegister(t, body, http.StatusOK)
}

func TestRegisterPlacement_LeafRefreshIsNotANewPlacement(t *testing.T) {
	e := newEnv(t)
	body := placementBody("pod-1", "", "")
	first := e.mustRegister(t, body, http.StatusCreated)

	// The same registration under the controller's renewed leaf, twice: a refresh, then a repeat.
	renewed, _ := clientWithLeaf(t, e.server, controller)
	for range 2 {
		status, raw, _ := e.registerPlacement(t, renewed, agentA, body)
		assert.Equal(t, http.StatusOK, status, string(raw))
		assert.Equal(t, first, raw, "a refresh changed the placement or its token")
	}

	// Control: the placement held is still pod-1, so a replacement naming it is registered.
	e.mustRegister(t, placementBody("pod-2", "pod-1", "evidence of pod-1"), http.StatusCreated)
}

func TestRegisterPlacement_AgentNotPlacedUnderTheEpochRefused(t *testing.T) {
	tests := []struct {
		name   string
		agent  string
		body   string
		setup  func(e *env)
		status int
		kind   string
	}{
		{"the latest revision is another controller's", agentC, placementBody("pod-1", "", ""),
			func(e *env) { e.prover.epochs[agentC] = epoch }, http.StatusForbidden, ""},
		{"garam refuses the agent's proof", agentB, placementBody("pod-1", "", ""),
			func(e *env) { delete(e.prover.epochs, agentB) }, http.StatusForbidden, ""},
		{"garam proves another epoch", agentB, placementBody("pod-1", "", ""),
			func(e *env) { e.prover.epochs[agentB] = "8" }, http.StatusConflict, "epoch_superseded"},
		{"garam proves the registration's epoch, not the revision's", agentB,
			strings.Replace(placementBody("pod-1", "", ""), `"epoch":"7"`, `"epoch":"8"`, 1),
			func(e *env) { e.prover.epochs[agentB] = "8" }, http.StatusConflict, "epoch_superseded"},
		{"the registration names another epoch", agentB, strings.Replace(placementBody("pod-1", "", ""), `"epoch":"7"`, `"epoch":"6"`, 1),
			func(*env) {}, http.StatusConflict, "epoch_superseded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			status, raw, out := e.registerPlacement(t, e.withCert, tt.agent, tt.body)
			assert.Equal(t, tt.status, status, string(raw))
			assert.Equal(t, tt.kind, out["kind"])

			// Control: agentA, proved placed here under its revision's epoch, is registered.
			e.mustRegister(t, placementBody("pod-1", "", ""), http.StatusCreated)
		})
	}
}

func TestRegisterPlacement_RefusesWhatIsNotOneRegistration(t *testing.T) {
	valid := placementBody("pod-1", "", "")
	tests := []struct {
		name string
		body string
	}{
		{"no epoch", strings.Replace(valid, `"epoch":"7"`, `"epoch":""`, 1)},
		{"no pod", strings.Replace(valid, `"podUid":"pod-1"`, `"podUid":""`, 1)},
		{"no claim", strings.Replace(valid, `"pvcUid":"pvc-1"`, `"pvcUid":""`, 1)},
		{"a token digest that is not one", strings.Replace(valid, digest("token of pod-1"), "TOKEN", 1)},
		{"an evidence digest that is not one", strings.Replace(placementBody("pod-1", "pod-0", "e"), digest("e"), "nope", 1)},
		{"a placement replacing itself", placementBody("pod-1", "pod-1", "evidence")},
		{"an unknown field", strings.Replace(valid, `{`, `{"assignee":"x",`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			status, raw, out := e.registerPlacement(t, e.withCert, agentA, tt.body)
			assert.Equal(t, http.StatusBadRequest, status, string(raw))
			assert.Equal(t, "invalid_request", out["kind"])

			// Control: the valid registration is stored.
			e.mustRegister(t, valid, http.StatusCreated)
		})
	}
}

func TestRegisterPlacement_ConcurrentReplacementsOfOnePlacementStoreOne(t *testing.T) {
	e := newEnv(t)
	e.mustRegister(t, placementBody("pod-1", "", ""), http.StatusCreated)

	const n = 8
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			status, _, _ := e.registerPlacement(t, e.withCert, agentA,
				placementBody("pod-new-"+string(rune('a'+i)), "pod-1", "evidence of pod-1"))
			statuses[i] = status
		})
	}
	wg.Wait()
	created := 0
	for _, status := range statuses {
		require.Contains(t, []int{http.StatusCreated, http.StatusConflict}, status)
		if status == http.StatusCreated {
			created++
		}
	}
	assert.Equal(t, 1, created, "more than one placement replaced the same one")
}
