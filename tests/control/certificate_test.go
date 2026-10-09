//go:build e2e

package control_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// managedAgent creates an agent through the console's create route, as #244's path does, and
// returns its GRN and epoch.
func managedAgent(t *testing.T) (agent, epoch string) {
	t.Helper()
	profile := publishProfile(t, real.orgID)
	requestID := name(t, "create")
	body := createAgentBody(requestID, real.controllerGRN, publishTemplate(t, real.orgID, profile), profile)
	authority, err := mintCreate(requestID, body)
	require.NoError(t, err)
	status, created := createThroughConsole(t, authority, body)
	require.Equal(t, http.StatusCreated, status, created)
	return created["agent"], created["epoch"]
}

// certificateRequestPEM is a PKCS#10 request over a fresh P-256 key, generated here as the
// manager generates it in the cluster, with an empty subject and no SAN.
func certificateRequestPEM(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func certificateRequestBody(requestID, epoch, csr string) string {
	b, err := json.Marshal(map[string]string{"requestId": requestID, "epoch": epoch, "certificateRequestPem": csr})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// requestCertificate posts body on the controller route for agent, presenting the controller's
// leaf, and returns the status and the raw answer.
func requestCertificate(agent, body string) (int, []byte, error) {
	resp, err := real.feedClient().Post(attachedURL+"/v1/operators/self/agents/"+agent+"/certificate-requests",
		"application/json", strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// parsePEM is the one certificate in text.
func parsePEM(t *testing.T, text string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(text))
	require.NotNil(t, block, text)
	c, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return c
}

func TestInitialCertificate_IssuedByGaramForTheControllersKey(t *testing.T) {
	agent, epoch := managedAgent(t)
	key, csr := certificateRequestPEM(t)
	body := certificateRequestBody(name(t, "certificate"), epoch, csr)

	status, first, err := requestCertificate(agent, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, string(first))
	var issued struct {
		Agent, Epoch, CertificatePem, IssuerPem, ServerRootPem, NotAfter string
	}
	require.NoError(t, json.Unmarshal(first, &issued))
	assert.Equal(t, agent, issued.Agent)
	assert.Equal(t, epoch, issued.Epoch)

	// The leaf names the agent's GRN as its one SAN URI, is over the key generated here, and
	// chains to the issuer garam answered, for client authentication.
	leaf := parsePEM(t, issued.CertificatePem)
	require.Len(t, leaf.URIs, 1)
	assert.Equal(t, agent, leaf.URIs[0].String())
	assert.True(t, key.PublicKey.Equal(leaf.PublicKey), "the leaf is not over the controller's key")
	roots := x509.NewCertPool()
	roots.AddCert(parsePEM(t, issued.IssuerPem))
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
	parsePEM(t, issued.ServerRootPem)

	// An identical retry is answered the identical bytes.
	status, repeat, err := requestCertificate(agent, body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status, string(repeat))
	assert.Equal(t, first, repeat)

	// A changed certificate request under the same request id is refused.
	_, other := certificateRequestPEM(t)
	var changed map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &changed))
	status, refused, err := requestCertificate(agent, certificateRequestBody(changed["requestId"], epoch, other))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, status, string(refused))
	assert.Contains(t, string(refused), "request_reused")
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM initial_certificates WHERE agent = $1", agent))
}

func TestInitialCertificate_ConcurrentIdenticalRequestsStoreOne(t *testing.T) {
	agent, epoch := managedAgent(t)
	_, csr := certificateRequestPEM(t)
	statuses, answers := sendConcurrently(t, agent, certificateRequestBody(name(t, "certificate"), epoch, csr), 16)

	created := 0
	for i, status := range statuses {
		require.Contains(t, []int{http.StatusCreated, http.StatusOK}, status, string(answers[i]))
		if status == http.StatusCreated {
			created++
		}
		assert.Equal(t, answers[0], answers[i])
	}
	assert.Equal(t, 1, created, "more than one request stored the certificate")
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM initial_certificates WHERE agent = $1", agent))
}

// TestInitialCertificate_GaramRefusalKeepsNothing has garam refuse every one of concurrent
// identical requests: the agent's stored creation reference is pointed at another agent's
// creation, which garam refuses with 403. Garam records nothing for a refusal, so neither does
// the store, and once the reference is restored a corrected request is issued.
func TestInitialCertificate_GaramRefusalKeepsNothing(t *testing.T) {
	agent, epoch := managedAgent(t)
	other, _ := managedAgent(t)
	const swap = `UPDATE creations SET operation_ref = (SELECT operation_ref FROM creations WHERE agent = $2)
WHERE agent = $1`
	var ref string
	const read = "SELECT operation_ref FROM creations WHERE agent = $1"
	require.NoError(t, pool.QueryRow(t.Context(), read, agent).Scan(&ref))
	require.NoError(t, execute(t, swap, agent, other))
	_, csr := certificateRequestPEM(t)

	statuses, answers := sendConcurrently(t, agent, certificateRequestBody(name(t, "refused"), epoch, csr), 16)
	for i, status := range statuses {
		assert.Equal(t, http.StatusForbidden, status, string(answers[i]))
		assert.Contains(t, string(answers[i]), `"kind"`)
	}
	assert.Equal(t, 0, count(t, "SELECT count(*) FROM initial_certificates WHERE agent = $1", agent),
		"a refused request was kept")

	// Control: with the agent's own reference, a corrected request is issued.
	require.NoError(t, execute(t, "UPDATE creations SET operation_ref = $2 WHERE agent = $1", agent, ref))
	_, corrected := certificateRequestPEM(t)
	status, raw, err := requestCertificate(agent, certificateRequestBody(name(t, "corrected"), epoch, corrected))
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, status, string(raw))
}

// TestInitialCertificate_AnAgentWithNoCreationIsNotFound answers 404 for an agent its controller
// is assigned but that no creation names.
func TestInitialCertificate_AnAgentWithNoCreationIsNotFound(t *testing.T) {
	g := requireGaram(t)
	profile := seedRevision(t, g)
	// A configure records the latest revision for the controller garam assigned the agent to,
	// which the route requires before it looks for a creation.
	status, _ := configureKind(t, g, newConfigureRequest(name(t, "request"), profile, "configured", 1))
	require.Equal(t, http.StatusOK, status)
	_, csr := certificateRequestPEM(t)

	status, raw, err := requestCertificate(g.agent(), certificateRequestBody(name(t, "certificate"), g.assignment, csr))
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, status, string(raw))
}

// sendConcurrently sends body for agent from n requests at once.
func sendConcurrently(t *testing.T, agent, body string, n int) ([]int, [][]byte) {
	t.Helper()
	statuses, answers := make([]int, n), make([][]byte, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			status, raw, err := requestCertificate(agent, body)
			if err != nil {
				status, raw = -1, []byte(err.Error())
			}
			statuses[i], answers[i] = status, raw
		})
	}
	wg.Wait()
	return statuses, answers
}
