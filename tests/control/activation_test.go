//go:build e2e

package control_test

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executionContract is agent-execution.v1 (garam@e81a1e0, ADR-0084), carried by the agent routes.
const executionContract = "agent-execution.v1"

// placedAgent is a managed agent with its first certificate issued over a key generated here, and
// a placement registered on its controller under token.
type placedAgent struct {
	grn, epoch, token string
	pair              tls.Certificate
}

func placeAgent(t *testing.T) placedAgent {
	t.Helper()
	grn, epoch := managedAgent(t)
	key, csr := certificateRequestPEM(t)
	status, raw, err := requestCertificate(grn, certificateRequestBody(name(t, "certificate"), epoch, csr))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var issued struct{ CertificatePem string }
	require.NoError(t, json.Unmarshal(raw, &issued))
	pair := tls.Certificate{Certificate: [][]byte{parsePEM(t, issued.CertificatePem).Raw}, PrivateKey: key}

	token := name(t, "placement-token")
	body, err := json.Marshal(struct {
		Epoch       string  `json:"epoch"`
		PodUID      string  `json:"podUid"`
		PVCUID      string  `json:"pvcUid"`
		TokenSHA256 string  `json:"tokenSha256"`
		Previous    *string `json:"previous"`
	}{epoch, "pod-1", "pvc-1", sha(token), nil})
	require.NoError(t, err)
	mustPlace(t, grn, string(body), http.StatusCreated)
	return placedAgent{grn: grn, epoch: epoch, token: token, pair: pair}
}

// adapterClient is the agent's adapter's client of control: it presents pair and verifies control's
// serving certificate.
func adapterClient(pair tls.Certificate) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      apiClient.Transport.(*http.Transport).TLSClientConfig.RootCAs,
		Certificates: []tls.Certificate{pair},
	}}}
}

// agentAnswer is one of the agent routes' answers.
type agentAnswer struct {
	status   int
	contract string
	body     map[string]any
	raw      string
}

// postAgent sends body to the agent's route, as its adapter does.
func postAgent(t *testing.T, a placedAgent, pair tls.Certificate, route, body string) agentAnswer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, attachedURL+"/v1/agents/"+a.grn+"/"+route, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Garam-Contract-Version", executionContract)
	if route == "activations" {
		req.Header.Set("Authorization", "Garam-Placement "+a.token)
	}
	resp, err := adapterClient(pair).Do(req)
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

// renew has garam renew the agent's certificate over the connection its current one authenticates,
// presenting its execution token, as the agent renews its own (issueAgentCertificate).
func renew(t *testing.T, a placedAgent, pair tls.Certificate, executionToken string) tls.Certificate {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      real.machine.Transport.(*http.Transport).TLSClientConfig.RootCAs,
		Certificates: []tls.Certificate{pair},
	}}}
	req, err := http.NewRequest(http.MethodPost, real.machineURL+"/agents/"+a.grn+"/certificate", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Execution "+executionToken)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	var renewed struct{ CertificatePem, PrivateKeyPem string }
	require.NoError(t, json.Unmarshal(raw, &renewed))
	out, err := tls.X509KeyPair([]byte(renewed.CertificatePem), []byte(renewed.PrivateKeyPem))
	require.NoError(t, err)
	return out
}

func TestActivation_AgainstGaramsExecutionFence(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("a", 32)
	body := activationOf(name(t, "activation"), a.epoch, generation)

	// The first activation, under the creation's reference.
	first := postAgent(t, a, a.pair, "activations", body)
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	assert.Equal(t, executionContract, first.contract)
	activation, _ := first.body["activationId"].(string)
	require.NotEmpty(t, activation)
	assert.Equal(t, float64(1), first.body["tokenVersion"])
	assert.Equal(t, a.grn, first.body["grn"])
	assert.Equal(t, "1", first.body["configRevision"])

	// An identical retry: the same activation, a newer token.
	retry := postAgent(t, a, a.pair, "activations", body)
	require.Equal(t, http.StatusOK, retry.status, retry.raw)
	assert.Equal(t, activation, retry.body["activationId"])
	assert.Equal(t, float64(2), retry.body["tokenVersion"])

	// Runtime status: accepted for the activated generation only.
	accepted := postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, generation))
	assert.Equal(t, http.StatusNoContent, accepted.status, accepted.raw)
	assert.Equal(t, executionContract, accepted.contract)
	other := postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, strings.Repeat("b", 32)))
	assert.Equal(t, http.StatusConflict, other.status, other.raw)
	assert.Equal(t, "generation_not_current", other.body["kind"])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM agent_status WHERE agent = $1 AND applied_revision = 1", a.grn))

	// Renewal: the agent renews under its execution token, and the same request under the renewed
	// leaf remints the same activation.
	renewed := renew(t, a, a.pair, retry.body["token"].(string))
	again := postAgent(t, a, renewed, "activations", body)
	require.Equal(t, http.StatusOK, again.status, again.raw)
	assert.Equal(t, activation, again.body["activationId"])
	assert.Equal(t, float64(3), again.body["tokenVersion"])

	// An ended activation: garam ends it, and its replay is refused.
	require.NoError(t, real.machineCall(real.hostedTLS, http.MethodPost,
		"/agents/"+a.grn+"/activations/"+activation+"/deactivation", "execution-fence.v1", nil, http.StatusOK, nil))
	ended := postAgent(t, a, renewed, "activations", body)
	assert.Equal(t, http.StatusConflict, ended.status, ended.raw)
	assert.Equal(t, "activation_superseded", ended.body["kind"])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM activation_requests WHERE agent = $1", a.grn),
		"a retry stored a second activation request")
}

// recover replaces the agent's credential lineage as an administrator's handoff lets the hosted
// operator (recoverAgentCredential), over a key generated here, and returns the recovered pair.
func recoverCredential(t *testing.T, a placedAgent) tls.Certificate {
	t.Helper()
	key, csr := certificateRequestPEM(t)
	requestID := name(t, "recovery")
	body, err := json.Marshal(struct {
		RequestID             string `json:"requestId"`
		Epoch                 string `json:"epoch"`
		CertificateRequestPEM string `json:"certificateRequestPem"`
	}{requestID, a.epoch, csr})
	require.NoError(t, err)
	authority, _, err := real.mintAuthority("agent:recover", a.grn, requestID, sha(string(body)))
	require.NoError(t, err)
	target := real.machineURL + "/agents/" + a.grn + "/credential-recovery"
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Garam-Contract-Version", "execution-fence.v1")
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := real.hostedTLS.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))
	var recovered struct{ CertificatePem string }
	require.NoError(t, json.Unmarshal(raw, &recovered))
	return tls.Certificate{Certificate: [][]byte{parsePEM(t, recovered.CertificatePem).Raw}, PrivateKey: key}
}

func TestActivation_RecoveredLineageRefused(t *testing.T) {
	a := placeAgent(t)
	generation := strings.Repeat("c", 32)
	body := activationOf(name(t, "activation"), a.epoch, generation)
	first := postAgent(t, a, a.pair, "activations", body)
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	// The runtime reports revision 1 serving, so a later generation needs no reference.
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, generation)).status)

	recovered := recoverCredential(t, a)

	// The old lineage's leaf is no longer the agent's credential.
	old := postAgent(t, a, a.pair, "activations", body)
	assert.Equal(t, http.StatusForbidden, old.status, old.raw)
	assert.Equal(t, "credential_fenced", old.body["kind"])
	// The recovery ended the activation, so its replay under the recovered leaf is refused too.
	ended := postAgent(t, a, recovered, "activations", body)
	assert.Equal(t, http.StatusConflict, ended.status, ended.raw)
	assert.Equal(t, "activation_superseded", ended.body["kind"])

	// Control: the runtime's next generation under the recovered leaf is a new activation.
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
	first := postAgent(t, a, a.pair, "activations", activationOf(name(t, "activation"), a.epoch, reported))
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	activation := first.body["activationId"].(string)
	require.Equal(t, http.StatusNoContent,
		postAgent(t, a, a.pair, "runtime-status", statusOf(activation, a.grn, a.epoch, reported)).status)

	kinds := make([]string, 2)
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i, generation := range []string{strings.Repeat("1", 32), strings.Repeat("2", 32)} {
		body := activationOf(name(t, "activation"), a.epoch, generation)
		wg.Go(func() {
			answer := postAgent(t, a, a.pair, "activations", body)
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
