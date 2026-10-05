//go:build e2e

package control_test

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

// sha is a SHA-256 as the manager writes one, of what.
func sha(what string) string {
	sum := sha256.Sum256([]byte(what))
	return hex.EncodeToString(sum[:])
}

// placementOf is a registration of pod at epoch, replacing previous where it names one.
func placementOf(epoch, pod, previous string) string {
	in := map[string]any{
		"epoch": epoch, "podUid": pod, "pvcUid": "pvc-" + pod, "tokenSha256": sha("token " + pod), "previous": nil,
	}
	if previous != "" {
		in["previous"] = map[string]string{"podUid": previous, "writerStoppedSha256": sha("evidence " + previous)}
	}
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// registerPlacement posts body on the controller route for agent, presenting the controller's
// leaf, and returns the status and the raw answer.
func registerPlacement(agent, body string) (int, []byte, error) {
	resp, err := real.feedClient().Post(attachedURL+"/v1/operators/self/agents/"+agent+"/placements",
		"application/json", strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func mustPlace(t *testing.T, agent, body string, status int) []byte {
	t.Helper()
	got, raw, err := registerPlacement(agent, body)
	require.NoError(t, err)
	require.Equal(t, status, got, string(raw))
	return raw
}

// currentPlacement is the Pod of the agent's current placement, and the leaf stored with it.
func currentPlacement(t *testing.T, agent string) (pod string, leaf []byte) {
	t.Helper()
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT pod_uid, leaf_der FROM placements WHERE agent = $1 AND revoked_at IS NULL", agent).Scan(&pod, &leaf))
	return pod, leaf
}

func TestPlacement_RegisteredUnderTheAgentBoundProofAgainstGaram(t *testing.T) {
	agent, epoch := managedAgent(t)

	// A first placement, stored with the controller's leaf as presented, and answered the same on a repeat.
	first := mustPlace(t, agent, placementOf(epoch, "pod-1", ""), http.StatusCreated)
	assert.Equal(t, first, mustPlace(t, agent, placementOf(epoch, "pod-1", ""), http.StatusOK))
	pod, leaf := currentPlacement(t, agent)
	assert.Equal(t, "pod-1", pod)
	assert.Equal(t, real.controller.Certificate[0], leaf, "the stored leaf is not the one presented")

	// Its replacement, which revokes it in the same step.
	second := mustPlace(t, agent, placementOf(epoch, "pod-2", "pod-1"), http.StatusCreated)
	pod, _ = currentPlacement(t, agent)
	assert.Equal(t, "pod-2", pod)
	const revoked = "SELECT count(*) FROM placements WHERE agent = $1 AND pod_uid = 'pod-1' AND revoked_at IS NOT NULL"
	assert.Equal(t, 1, count(t, revoked, agent))

	// A replay of the replaced registration, refused and reviving nothing.
	status, raw, err := registerPlacement(agent, placementOf(epoch, "pod-1", ""))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status, string(raw))
	assert.Contains(t, string(raw), "placement_superseded")
	pod, _ = currentPlacement(t, agent)
	assert.Equal(t, "pod-2", pod)

	// The manager's retry after a crash, and a refresh under the same leaf: both answered as stored.
	for range 2 {
		assert.Equal(t, second, mustPlace(t, agent, placementOf(epoch, "pod-2", "pod-1"), http.StatusOK))
	}
	assert.Equal(t, 2, count(t, "SELECT count(*) FROM placements WHERE agent = $1", agent), "a repeat stored a placement")

	// A registration under another epoch than garam proves, refused.
	status, raw, err = registerPlacement(agent, placementOf(epoch+"0", "pod-3", "pod-2"))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status, string(raw))
	assert.Contains(t, string(raw), "epoch_superseded")
}

func TestPlacement_ConcurrentReplacementsOfOnePlacementStoreOne(t *testing.T) {
	agent, epoch := managedAgent(t)
	mustPlace(t, agent, placementOf(epoch, "pod-1", ""), http.StatusCreated)

	const n = 8
	statuses := make([]int, n)
	answers := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			status, raw, err := registerPlacement(agent, placementOf(epoch, "pod-new-"+string(rune('a'+i)), "pod-1"))
			if err != nil {
				status, raw = -1, []byte(err.Error())
			}
			statuses[i], answers[i] = status, string(raw)
		})
	}
	wg.Wait()
	created := 0
	for i, status := range statuses {
		if status == http.StatusCreated {
			created++
			continue
		}
		assert.Equal(t, http.StatusConflict, status, answers[i])
		assert.Contains(t, answers[i], "previous_mismatch")
	}
	assert.Equal(t, 1, created, "more than one placement replaced the same one")
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM placements WHERE agent = $1 AND revoked_at IS NULL", agent))
}

func TestPlacement_ConcurrentIdenticalFirstRegistrationsStoreOne(t *testing.T) {
	agent, epoch := managedAgent(t)
	body := placementOf(epoch, "pod-1", "")

	const n = 8
	statuses := make([]int, n)
	answers := make([][]byte, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			status, raw, err := registerPlacement(agent, body)
			if err != nil {
				status, raw = -1, []byte(err.Error())
			}
			statuses[i], answers[i] = status, raw
		})
	}
	wg.Wait()
	created := 0
	for i, status := range statuses {
		require.Contains(t, []int{http.StatusCreated, http.StatusOK}, status, string(answers[i]))
		if status == http.StatusCreated {
			created++
		}
		assert.Equal(t, answers[0], answers[i])
	}
	assert.Equal(t, 1, created, "more than one request stored the placement")
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM placements WHERE agent = $1", agent))
}
