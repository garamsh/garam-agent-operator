package distribution_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// csrPEM is a PKCS#10 request over a fresh key of the kind generate makes.
func csrPEM(t *testing.T, generate func() (crypto.Signer, error)) string {
	t.Helper()
	key, err := generate()
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func p256() (crypto.Signer, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

// certificateBody is a certificate request's body.
func certificateBody(requestID, requestEpoch, csr string) string {
	b, err := json.Marshal(map[string]string{"requestId": requestID, "epoch": requestEpoch, "certificateRequestPem": csr})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// requestCertificate posts body for agent with client, and returns the status, the raw answer and
// its decoded fields.
func (e *env) requestCertificate(t *testing.T, client *http.Client, agent, body string) (int, []byte, map[string]string) {
	t.Helper()
	resp, err := client.Post(e.server.URL+"/v1/operators/self/agents/"+agent+"/certificate-requests",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, raw, out
}

func TestRequestCertificate_IssuesOnceAndAnswersTheStoredResult(t *testing.T) {
	e := newEnv(t)
	body := certificateBody("c1", epoch, csrPEM(t, p256))

	status, first, out := e.requestCertificate(t, e.withCert, agentA, body)
	require.Equal(t, http.StatusCreated, status, string(first))
	assert.Equal(t, map[string]string{
		"agent": agentA, "epoch": epoch, "certificatePem": "certificate for c1", "issuerPem": "issuer",
		"serverRootPem": "server root", "notAfter": "2026-11-05T12:00:00Z",
	}, out)
	require.Equal(t, 1, e.issuer.calls())
	assert.Equal(t, "create-ref-"+agentA, e.issuer.issuances[0].OperationRef)

	status, repeat, _ := e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusOK, status, string(repeat))
	assert.Equal(t, first, repeat, "an identical retry was answered other bytes")
	assert.Equal(t, 1, e.issuer.calls(), "an identical retry of an issued request asked garam again")
}

func TestRequestCertificate_RefusesWhatGaramWouldNotSignOver(t *testing.T) {
	valid := csrPEM(t, p256)
	block, _ := pem.Decode([]byte(valid))
	tampered := append([]byte{}, block.Bytes...)
	tampered[len(tampered)-1] ^= 0xff
	tests := []struct {
		name string
		body string
	}{
		{"not a PEM", certificateBody("c1", epoch, "not a certificate request")},
		{"two PEM blocks", certificateBody("c1", epoch, valid+valid)},
		{"another PEM type", certificateBody("c1", epoch, strings.ReplaceAll(valid, "CERTIFICATE REQUEST", "CERTIFICATE"))},
		{"a signature that does not verify", certificateBody("c1", epoch,
			string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered})))},
		{"a P-384 key", certificateBody("c1", epoch, csrPEM(t, func() (crypto.Signer, error) {
			return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		}))},
		{"an RSA key", certificateBody("c1", epoch, csrPEM(t, func() (crypto.Signer, error) {
			return rsa.GenerateKey(rand.Reader, 2048)
		}))},
		{"over 4 KiB", certificateBody("c1", epoch, valid+strings.Repeat(" ", 4097-len(valid)))},
		{"a request id garam does not take", certificateBody("c 1", epoch, valid)},
		{"no epoch", certificateBody("c1", "", valid)},
		{"an unknown field", `{"requestId":"c1","epoch":"7","certificateRequestPem":"","extra":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			status, raw, _ := e.requestCertificate(t, e.withCert, agentA, tt.body)
			assert.Equal(t, http.StatusBadRequest, status, string(raw))
			assert.Equal(t, 0, e.issuer.calls())

			// Control: the same request over the valid P-256 request is issued.
			status, raw, _ = e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, valid))
			assert.Equal(t, http.StatusCreated, status, string(raw))
		})
	}
}

func TestRequestCertificate_AgentNotPlacedHereRefused(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		setup func(e *env)
	}{
		{"latest revision recorded for another controller", agentC, func(e *env) { e.prover.epochs[agentC] = epoch }},
		{"garam refuses the agent's proof", agentB, func(e *env) { delete(e.prover.epochs, agentB) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			csr := csrPEM(t, p256)
			status, raw, _ := e.requestCertificate(t, e.withCert, tt.agent, certificateBody("c1", epoch, csr))
			assert.Equal(t, http.StatusForbidden, status, string(raw))
			assert.Equal(t, 0, e.issuer.calls())

			// Control: the same request for agentA, placed here under its epoch, is issued.
			status, raw, _ = e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csr))
			assert.Equal(t, http.StatusCreated, status, string(raw))
		})
	}
}

func TestRequestCertificate_AnotherEpochSuperseded(t *testing.T) {
	tests := []struct {
		name         string
		requestEpoch string
		setup        func(e *env)
	}{
		{"the request names another epoch", "6", func(*env) {}},
		{"garam proves another epoch than the revision's", epoch, func(e *env) { e.prover.epochs[agentB] = "8" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			csr := csrPEM(t, p256)
			status, raw, out := e.requestCertificate(t, e.withCert, agentB, certificateBody("c1", tt.requestEpoch, csr))
			assert.Equal(t, http.StatusConflict, status, string(raw))
			assert.Equal(t, "epoch_superseded", out["kind"])
			assert.Equal(t, 0, e.issuer.calls())

			// Control: agentA, proved under the epoch the request and its revision name, is issued.
			status, raw, _ = e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csr))
			assert.Equal(t, http.StatusCreated, status, string(raw))
		})
	}
}

func TestRequestCertificate_AnotherRequestThanTheStoredOneRefused(t *testing.T) {
	for _, stored := range []struct {
		name string
		err  error
	}{
		{"issued", nil},
		{"undecided", fmt.Errorf("no answer: %w", definition.ErrIssuanceUndecided)},
	} {
		t.Run(stored.name, func(t *testing.T) {
			e := newEnv(t)
			csr := csrPEM(t, p256)
			e.issuer.answer(stored.err)
			e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csr))
			e.issuer.answer(nil)

			for _, other := range []string{
				certificateBody("c1", epoch, csrPEM(t, p256)),
				certificateBody("c2", epoch, csr),
			} {
				status, raw, out := e.requestCertificate(t, e.withCert, agentA, other)
				assert.Equal(t, http.StatusConflict, status, string(raw))
				assert.Equal(t, "request_reused", out["kind"])
			}

			// Control: the stored request itself is answered with its certificate.
			status, raw, _ := e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csr))
			assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, status, string(raw))
		})
	}
}

func TestRequestCertificate_UndecidedIsRetriedAsTheSameRequest(t *testing.T) {
	e := newEnv(t)
	body := certificateBody("c1", epoch, csrPEM(t, p256))
	e.issuer.answer(fmt.Errorf("no answer: %w", definition.ErrIssuanceUndecided))

	status, raw, _ := e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusServiceUnavailable, status, string(raw))

	// Control: once garam answers, the same request is issued and sent unchanged.
	e.issuer.answer(nil)
	status, raw, _ = e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusCreated, status, string(raw))
	require.Equal(t, 2, e.issuer.calls())
	assert.Equal(t, e.issuer.issuances[0], e.issuer.issuances[1])
}

func TestRequestCertificate_GaramRefusalIsAnsweredWithItsKindAndKeepsNothing(t *testing.T) {
	tests := []struct {
		refusal definition.Refusal
		kind    string
		status  int
	}{
		{definition.RefusalForbidden, "permission_denied", http.StatusForbidden},
		{definition.RefusalConflict, "conflict", http.StatusConflict},
		{definition.RefusalInvalid, "invalid_argument", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			e := newEnv(t)
			e.issuer.answer(&definition.IssuanceRefusedError{Refusal: tt.refusal, Kind: tt.kind, Message: "refused"})
			status, raw, out := e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csrPEM(t, p256)))
			assert.Equal(t, tt.status, status, string(raw))
			assert.Equal(t, tt.kind, out["kind"])

			// Control: garam kept nothing, so a corrected request is sent and issued.
			e.issuer.answer(nil)
			status, raw, _ = e.requestCertificate(t, e.withCert, agentA, certificateBody("c2", epoch, csrPEM(t, p256)))
			assert.Equal(t, http.StatusCreated, status, string(raw))
		})
	}
}

// concurrentRequests sends body for agentA from n requests at once and returns their statuses and
// raw answers.
func (e *env) concurrentRequests(t *testing.T, n int, body string) ([]int, [][]byte) {
	t.Helper()
	statuses, answers := make([]int, n), make([][]byte, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			resp, err := e.withCert.Post(e.server.URL+"/v1/operators/self/agents/"+agentA+"/certificate-requests",
				"application/json", strings.NewReader(body))
			if err != nil {
				statuses[i] = -1
				return
			}
			defer func() { _ = resp.Body.Close() }()
			statuses[i] = resp.StatusCode
			answers[i], _ = io.ReadAll(resp.Body)
		})
	}
	wg.Wait()
	return statuses, answers
}

func TestRequestCertificate_ConcurrentIdenticalRequestsAgree(t *testing.T) {
	t.Run("garam refuses: every request is refused and nothing is kept", func(t *testing.T) {
		e := newEnv(t)
		e.issuer.answer(&definition.IssuanceRefusedError{Refusal: definition.RefusalForbidden, Kind: "permission_denied", Message: "no"})
		statuses, answers := e.concurrentRequests(t, 16, certificateBody("c1", epoch, csrPEM(t, p256)))
		for i, status := range statuses {
			assert.Equal(t, http.StatusForbidden, status, string(answers[i]))
		}

		// Control: a corrected request is not refused as another request than one kept.
		e.issuer.answer(nil)
		status, raw, _ := e.requestCertificate(t, e.withCert, agentA, certificateBody("c2", epoch, csrPEM(t, p256)))
		assert.Equal(t, http.StatusCreated, status, string(raw))
	})
	t.Run("garam issues: one request stores it, and every answer is its bytes", func(t *testing.T) {
		e := newEnv(t)
		statuses, answers := e.concurrentRequests(t, 16, certificateBody("c1", epoch, csrPEM(t, p256)))
		created := 0
		for i, status := range statuses {
			require.Contains(t, []int{http.StatusCreated, http.StatusOK}, status, string(answers[i]))
			if status == http.StatusCreated {
				created++
			}
			assert.Equal(t, answers[0], answers[i])
		}
		assert.Equal(t, 1, created, "more than one request stored the certificate")
	})
}

func TestRequestCertificate_NeedsAClientCertificate(t *testing.T) {
	e := newEnv(t)
	body := certificateBody("c1", epoch, csrPEM(t, p256))
	status, raw, _ := e.requestCertificate(t, e.withoutCert, agentA, body)
	assert.Equal(t, http.StatusUnauthorized, status, string(raw))

	// Control: the same request presenting the controller's leaf is issued.
	status, raw, _ = e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusCreated, status, string(raw))
}
