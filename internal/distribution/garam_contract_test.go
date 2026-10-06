package distribution_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
	garamprover "github.com/garamsh/garam-agent-operator/internal/distribution/prover"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// kindGaramContractUnsupported is the kind a garam answer under a contract this service does not
// take, or under none, is refused under.
const kindGaramContractUnsupported = "garam_contract_unsupported"

// proofGaram stands in for garam's controller proof: it proves the controller, and each agent
// asked about at epoch, under contract, or under no contract header where omit.
func proofGaram(t *testing.T, contract string, omit bool) *garammachine.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Agent *string `json:"agent"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		proof := map[string]any{"operator": controller, "org": org}
		if in.Agent != nil {
			proof["agent"] = map[string]string{"grn": *in.Agent, "epoch": epoch}
		}
		if !omit {
			w.Header().Set("Garam-Contract-Version", contract)
		}
		_ = json.NewEncoder(w).Encode(proof)
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return machine
}

func TestDesired_GaramsProofUnderAnotherContractOrNoneIsRefused503(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contract string
		omit     bool
		status   int
		kind     string
	}{
		{name: "the contract asked", contract: garammachine.OperationAuthority, status: http.StatusOK},
		{name: "another contract", contract: "operation-authority.v2", status: http.StatusServiceUnavailable,
			kind: kindGaramContractUnsupported},
		{name: "no contract", omit: true, status: http.StatusServiceUnavailable, kind: kindGaramContractUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			// The controller routes over garam's real proof port, against the double.
			server := httptest.NewUnstartedServer(distribution.NewHandler(distribution.Config{
				Definitions:  e.definitions,
				Prover:       garamprover.NewGaram(proofGaram(t, tc.contract, tc.omit)),
				PollInterval: 10 * time.Millisecond,
				MaxAgents:    10,
				Logger:       slog.New(slog.DiscardHandler),
			}))
			server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
			server.StartTLS()
			t.Cleanup(server.Close)
			e.server = server
			client, _ := clientWithLeaf(t, server, controller)

			got := e.desired(t, client, "")
			assert.Equal(t, tc.status, got.status, got.message)
			assert.Equal(t, tc.kind, got.kind)
			if tc.status == http.StatusOK {
				assert.Equal(t, map[string]string{agentA: "2", agentB: "2"}, got.agents)
			} else {
				assert.Empty(t, got.agents, "an unread answer released something")
			}
		})
	}
}

func TestRequestCertificate_GaramsIssuanceUnderAnotherContractIsRefused503AndRetriedAsTheSameRequest(t *testing.T) {
	e := newEnv(t)
	body := certificateBody("c1", epoch, csrPEM(t, p256))
	e.issuer.answer(fmt.Errorf("issue initial certificate: %w", definition.ErrGaramContractUnsupported))

	status, raw, out := e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusServiceUnavailable, status, string(raw))
	assert.Equal(t, kindGaramContractUnsupported, out["kind"])

	// Control: once garam answers under the contract, the same request is issued: nothing was
	// cleared or decided by the unread answer.
	e.issuer.answer(nil)
	status, raw, _ = e.requestCertificate(t, e.withCert, agentA, body)
	assert.Equal(t, http.StatusCreated, status, string(raw))
}
