//go:build e2e

package control_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executionContract is agent-execution.v2 (garam@afde8b8, ADR-0084, ADR-0100), carried by the
// agent routes.
const executionContract = "agent-execution.v2"

// placedAgent is a managed agent created through the binary, with a placement registered on its
// controller under token, and control, the agent's token its controller minted for this service.
type placedAgent struct {
	grn, epoch, token, control string
}

func placeAgent(t *testing.T) placedAgent {
	t.Helper()
	grn, epoch := managedAgent(t)
	a := placedAgent{grn: grn, epoch: epoch, token: name(t, "placement-token"), control: controlToken(t, grn)}
	mustPlace(t, grn, a.placement(t), http.StatusCreated)
	return a
}

// controlToken is a token of agent its controller minted for this service, as the manager keeps
// one in the file its adapter reads.
func controlToken(t *testing.T, agent string) string {
	t.Helper()
	token, err := real.mintAgentToken(agent, real.hosted)
	require.NoError(t, err)
	return token
}

// placement is the body a's controller registers its placement with.
func (a placedAgent) placement(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Epoch       string  `json:"epoch"`
		PodUID      string  `json:"podUid"`
		PVCUID      string  `json:"pvcUid"`
		TokenSHA256 string  `json:"tokenSha256"`
		Previous    *string `json:"previous"`
	}{a.epoch, "pod-1", "pvc-1", sha(a.token), nil})
	require.NoError(t, err)
	return string(body)
}

// agentAnswer is one of the agent routes' answers.
type agentAnswer struct {
	status   int
	contract string
	body     map[string]any
	raw      string
}

// postAgent sends body to the agent's route under control, as its adapter does.
func postAgent(t *testing.T, a placedAgent, control, route, body string) agentAnswer {
	t.Helper()
	return postAgentAt(t, attachedURL, a, control, route, body)
}

// postAgentAt is postAgent to the binary whose routes are at base. The adapter presents its token
// in Authorization and, on an activation, the placement token in Garam-Placement, over server TLS.
func postAgentAt(t *testing.T, base string, a placedAgent, control, route, body string) agentAnswer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/agents/"+a.grn+"/"+route, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Garam-Contract-Version", executionContract)
	req.Header.Set("Authorization", "Garam-Token "+control)
	if route == "activations" {
		req.Header.Set("Garam-Placement", a.token)
	}
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := agentAnswer{
		status: resp.StatusCode, contract: resp.Header.Get("Garam-Contract-Version"),
		body: map[string]any{}, raw: string(raw),
	}
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out.body), string(raw))
	}
	return out
}

func activationOf(requestID, epoch, generation string) string {
	b, err := json.Marshal(struct {
		RequestID      string `json:"requestId"`
		Epoch          string `json:"epoch"`
		Generation     string `json:"generation"`
		ConfigRevision string `json:"configRevision"`
	}{requestID, epoch, generation, "1"})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func statusOf(activation, grn, epoch, generation string) string {
	b, err := json.Marshal(struct {
		ActivationID   string `json:"activationId"`
		GRN            string `json:"grn"`
		Epoch          string `json:"epoch"`
		Generation     string `json:"generation"`
		ConfigRevision string `json:"configRevision"`
		State          string `json:"state"`
		StartedAt      string `json:"startedAt"`
		ObservedAt     string `json:"observedAt"`
	}{activation, grn, epoch, generation, "1", "serving", "2026-10-05T10:00:00Z", "2026-10-05T10:00:05Z"})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestActivation_AgainstGaramsExecutionFence(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("a", 32)
	body := activationOf(name(t, "activation"), a.epoch, generation)

	// The first activation, under the creation's reference.
	first := postAgent(t, a, a.control, "activations", body)
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	assert.Equal(t, executionContract, first.contract)
	activation, _ := first.body["activationId"].(string)
	require.NotEmpty(t, activation)
	assert.Equal(t, float64(1), first.body["tokenVersion"])
	assert.Equal(t, a.grn, first.body["grn"])
	assert.Equal(t, "1", first.body["configRevision"])

	// An identical retry: the same activation, a newer token.
	retry := postAgent(t, a, a.control, "activations", body)
	require.Equal(t, http.StatusOK, retry.status, retry.raw)
	assert.Equal(t, activation, retry.body["activationId"])
	assert.Equal(t, float64(2), retry.body["tokenVersion"])

	// The controller reports what it rendered, so the runtime's report updates a status it did not create.
	controllerStatus, err := real.feedClient().Post(attachedURL+"/v1/operators/self/agents/"+a.grn+"/status",
		"application/json", strings.NewReader(`{"observedRevision":"1","renderedRevision":"1"}`))
	require.NoError(t, err)
	_ = controllerStatus.Body.Close()
	require.Equal(t, http.StatusOK, controllerStatus.StatusCode)
	require.Equal(t, 1, count(t, "SELECT count(*) FROM agent_status WHERE agent = $1 AND applied_revision IS NULL", a.grn))

	// Runtime status: accepted for the activated generation only.
	accepted := postAgent(t, a, a.control, "runtime-status", statusOf(activation, a.grn, a.epoch, generation))
	assert.Equal(t, http.StatusNoContent, accepted.status, accepted.raw)
	assert.Equal(t, executionContract, accepted.contract)
	other := postAgent(t, a, a.control, "runtime-status", statusOf(activation, a.grn, a.epoch, strings.Repeat("b", 32)))
	assert.Equal(t, http.StatusConflict, other.status, other.raw)
	assert.Equal(t, "generation_not_current", other.body["kind"])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM agent_status WHERE agent = $1 AND applied_revision = 1", a.grn))

	// Refresh: the controller mints the agent's next token, and the same request under it remints
	// the same activation.
	refreshed := controlToken(t, a.grn)
	again := postAgent(t, a, refreshed, "activations", body)
	require.Equal(t, http.StatusOK, again.status, again.raw)
	assert.Equal(t, activation, again.body["activationId"])
	assert.Equal(t, float64(3), again.body["tokenVersion"])

	// A token minted for garam, forwarded to this service, is not the agent's token for it.
	garamToken, err := real.mintAgentToken(a.grn, "")
	require.NoError(t, err)
	forwarded := postAgent(t, a, garamToken, "activations", body)
	assert.Equal(t, http.StatusForbidden, forwarded.status, forwarded.raw)
	assert.Equal(t, "credential_fenced", forwarded.body["kind"])

	// An ended activation: garam ends it, and its replay is refused.
	require.NoError(t, real.machineCall(real.hostedMachine, "/agents/"+a.grn+"/activations/"+activation+"/deactivation",
		fenceContract, nil, http.StatusOK, nil))
	ended := postAgent(t, a, refreshed, "activations", body)
	assert.Equal(t, http.StatusConflict, ended.status, ended.raw)
	assert.Equal(t, "activation_superseded", ended.body["kind"])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM activation_requests WHERE agent = $1", a.grn),
		"a retry stored a second activation request")
}

// recoverCredential replaces the agent's lineage as an administrator's handoff lets the hosted
// operator (recoverAgentCredential under execution-fence.v2), and returns a control token the
// controller minted in the new lineage.
func recoverCredential(t *testing.T, a placedAgent) string {
	t.Helper()
	requestID := name(t, "recovery")
	body, err := json.Marshal(struct {
		RequestID string `json:"requestId"`
		Epoch     string `json:"epoch"`
	}{requestID, a.epoch})
	require.NoError(t, err)
	authority, _, err := real.mintAuthority("agent:recover", a.grn, requestID, sha(string(body)))
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, real.machineURL+"/agents/"+a.grn+"/credential-recovery",
		strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Garam-Contract-Version", fenceContract)
	req.Header.Set("Garam-Operation", authority)
	resp, err := real.hostedMachine.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	var recovered struct{ GRN, Lineage string }
	require.NoError(t, json.Unmarshal(raw, &recovered))
	require.Equal(t, a.grn, recovered.GRN)
	require.NotEmpty(t, recovered.Lineage)
	return controlToken(t, a.grn)
}

func TestActivation_RecoveredLineageRefused(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("c", 32)
	body := activationOf(name(t, "activation"), a.epoch, generation)
	first := postAgent(t, a, a.control, "activations", body)
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	// The runtime reports revision 1 serving, so a later generation needs no reference.
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.control, "runtime-status", statusOf(activation, a.grn, a.epoch, generation)).status)

	recovered := recoverCredential(t, a)

	// The old lineage's token is no longer the agent's.
	old := postAgent(t, a, a.control, "activations", body)
	assert.Equal(t, http.StatusForbidden, old.status, old.raw)
	assert.Equal(t, "credential_fenced", old.body["kind"])
	// The recovery ended the activation, so its replay under the new lineage's token is refused too.
	ended := postAgent(t, a, recovered, "activations", body)
	assert.Equal(t, http.StatusConflict, ended.status, ended.raw)
	assert.Equal(t, "activation_superseded", ended.body["kind"])

	// Control: the runtime's next generation under the new lineage's token is a new activation.
	nextBody := activationOf(name(t, "activation"), a.epoch, strings.Repeat("d", 32))
	next := postAgent(t, a, recovered, "activations", nextBody)
	assert.Equal(t, http.StatusCreated, next.status, next.raw)
	assert.NotEqual(t, activation, next.body["activationId"])
}

// TestActivation_ConcurrentGenerationsAreSerialized has two runtime generations activated at once.
// One at a time, the second reads the first as its anchor. Revision 1 was reported effective only
// under the activation before both, so for the second it is pending and is sent under the
// creation's reference, which garam takes for a first activation alone: 403 (#218, #1167).
// Unserialized, both would read the same anchor and garam would refuse one as superseded.
func TestActivation_ConcurrentGenerationsAreSerialized(t *testing.T) {
	a := placeAgent(t)
	reported := strings.Repeat("e", 32)
	first := postAgent(t, a, a.control, "activations", activationOf(name(t, "activation"), a.epoch, reported))
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.control, "runtime-status", statusOf(activation, a.grn, a.epoch, reported)).status)

	kinds := make([]string, 2)
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i, generation := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		body := activationOf(name(t, "activation"), a.epoch, generation)
		wg.Go(func() {
			answer := postAgent(t, a, a.control, "activations", body)
			statuses[i] = answer.status
			kinds[i], _ = answer.body["kind"].(string)
		})
	}
	wg.Wait()
	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusForbidden}, statuses)
	assert.ElementsMatch(t, []string{"", "not_authorized"}, kinds)

	// The anchors chain: one on the reported activation, the other on the one activated first.
	rows, err := pool.Query(t.Context(), `SELECT replaces_activation_id, COALESCE(activation_id, '')
FROM activation_requests WHERE agent = $1 AND generation <> $2`, a.grn, reported)
	require.NoError(t, err)
	anchors := map[string]string{}
	for rows.Next() {
		var anchor, activated string
		require.NoError(t, rows.Scan(&anchor, &activated))
		anchors[anchor] = activated
	}
	require.NoError(t, rows.Err())
	require.Len(t, anchors, 2, "both requests were sent under the same anchor")
	activatedFirst := anchors[activation]
	require.NotEmpty(t, activatedFirst)
	assert.Contains(t, anchors, activatedFirst)
}
