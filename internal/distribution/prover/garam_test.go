package prover_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/distribution"
	"github.com/garamsh/garam-agent-operator/internal/distribution/prover"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const leafPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

// sent is what the stand-in garam last received.
type sent struct {
	path string
	body map[string]any
}

func prove(t *testing.T, status int, answer, agent string) (distribution.Proof, sent, error) {
	t.Helper()
	var got sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Garam-Contract-Version", "operation-authority.v1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	p, err := prover.NewGaram(machine).Prove(context.Background(), "grn:root:default:operator:k8s", []byte(leafPEM), agent)
	return p, got, err
}

func TestGaram_ForwardsTheLeafAndReadsTheProof(t *testing.T) {
	p, got, err := prove(t, 200,
		`{"operator":"grn:root:default:operator:k8s","org":"grn:root:default:org:acme","agent":{"grn":"grn:acme:default:agent:a","epoch":"7"}}`,
		"grn:acme:default:agent:a")
	require.NoError(t, err)
	assert.Equal(t, "/operators/grn:root:default:operator:k8s/introspection", got.path)
	assert.Equal(t, leafPEM, got.body["certificatePem"])
	assert.Equal(t, "grn:acme:default:agent:a", got.body["agent"])
	assert.Equal(t, distribution.Proof{
		Operator: "grn:root:default:operator:k8s", Org: "grn:root:default:org:acme",
		Agent: &distribution.ProvenAgent{GRN: "grn:acme:default:agent:a", Epoch: "7"},
	}, p)
}

func TestGaram_ASessionProofNamesNoAgent(t *testing.T) {
	p, got, err := prove(t, 200, `{"operator":"grn:root:default:operator:k8s","org":"grn:root:default:org:acme","agent":null}`, "")
	require.NoError(t, err)
	agent, present := got.body["agent"]
	assert.True(t, present)
	assert.Nil(t, agent)
	assert.Nil(t, p.Agent)
}

func TestGaram_EveryRefusalIsNotProved(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			_, _, err := prove(t, status, `{"message":"refused"}`, "")
			require.ErrorIs(t, err, distribution.ErrNotProved)
		})
	}
	// Control: a 200 is a proof, not a refusal.
	_, _, err := prove(t, 200, `{"operator":"o","org":"g","agent":null}`, "")
	require.NoError(t, err)
}

func TestGaram_UndecidedIsUndecided(t *testing.T) {
	_, _, err := prove(t, http.StatusServiceUnavailable, `{"message":"internal error"}`, "")
	require.ErrorIs(t, err, distribution.ErrUndecided)
	assert.NotErrorIs(t, err, distribution.ErrNotProved)
}

func TestGaram_AnyOtherAnswerIsNeitherAProofNorARefusal(t *testing.T) {
	_, _, err := prove(t, http.StatusBadRequest, `{"message":"malformed"}`, "")
	require.Error(t, err)
	assert.NotErrorIs(t, err, distribution.ErrNotProved)
	assert.NotErrorIs(t, err, distribution.ErrUndecided)
}
