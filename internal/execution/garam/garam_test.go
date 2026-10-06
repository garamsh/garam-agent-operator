package garam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/execution"
	executiongaram "github.com/garamsh/garam-agent-operator/internal/execution/garam"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const (
	agent = "grn:acme:default:agent:a"
	// gen and leaf are the generation and the leaf every call here names.
	gen  = "gen"
	leaf = "leaf"
	// keyCertificatePEM is the leaf's member in every request garam is sent, and keyGeneration
	// the generation's.
	keyCertificatePEM = "certificatePem"
	keyGeneration     = "generation"
)

// sent is what the stand-in garam last received.
type sent struct {
	path     string
	contract string
	body     map[string]any
}

// stand is a stand-in garam answering status and answer under contract.
func stand(t *testing.T, contract string, status int, answer string) (*executiongaram.Garam, *sent) {
	t.Helper()
	got := &sent{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.contract = r.URL.EscapedPath(), r.Header.Get("Garam-Contract-Version")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Garam-Contract-Version", contract)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return executiongaram.NewGaram(machine), got
}

func TestGaram_IntrospectsUnderTheExecutionFence(t *testing.T) {
	g, got := stand(t, garammachine.ExecutionFence, http.StatusOK,
		`{"grn":"`+agent+`","org":"o","assignee":"c","epoch":"7","mode":"fenced","credential":"current","generation":"current","activationId":"a1"}`)
	i, err := g.Introspect(context.Background(), agent, []byte(leaf), gen)
	require.NoError(t, err)
	assert.Equal(t, execution.Introspection{GRN: agent, Org: "o", Assignee: "c", Epoch: "7", Mode: "fenced",
		Credential: "current", Generation: "current", ActivationID: "a1"}, i)
	assert.Equal(t, "/agents/"+agent+"/execution/introspection", got.path)
	assert.Equal(t, garammachine.ExecutionFence, got.contract)
	assert.Equal(t, map[string]any{keyCertificatePEM: leaf, keyGeneration: gen}, got.body)

	// Without a generation, none is sent.
	g, got = stand(t, garammachine.ExecutionFence, http.StatusOK, `{"activationId":null}`)
	_, err = g.Introspect(context.Background(), agent, []byte(leaf), "")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{keyCertificatePEM: leaf, keyGeneration: nil}, got.body)
}

func TestGaram_ActivatesUnderTheExecutionFence(t *testing.T) {
	answer := `{"activationId":"a1","token":"t","tokenVersion":2,"grn":"` + agent + `","epoch":"7","generation":"gen","lineage":null}`
	for status, created := range map[int]bool{http.StatusCreated: true, http.StatusOK: false} {
		g, got := stand(t, garammachine.ExecutionFence, status, answer)
		a, wasCreated, err := g.Activate(context.Background(), agent, execution.ActivationCall{
			RequestID: "r", Epoch: "7", Generation: gen, CertificatePEM: []byte(leaf),
		})
		require.NoError(t, err)
		assert.Equal(t, created, wasCreated)
		assert.Equal(t, execution.Activation{ActivationID: "a1", Token: "t", TokenVersion: 2, GRN: agent, Epoch: "7", Generation: gen}, a)
		assert.Equal(t, "/agents/"+agent+"/activations", got.path)
		// An empty anchor and reference are sent as null, which garam reads as none.
		assert.Equal(t, map[string]any{"requestId": "r", "epoch": "7", keyGeneration: gen,
			"replacesActivationId": nil, "operationRef": nil, keyCertificatePEM: leaf}, got.body)
	}
}

func TestGaram_ProvesTheControllerUnderOperationAuthority(t *testing.T) {
	g, got := stand(t, garammachine.OperationAuthority, http.StatusOK,
		`{"operator":"c","org":"o","agent":{"grn":"`+agent+`","epoch":"7"}}`)
	proof, err := g.ProveController(context.Background(), "c", []byte(leaf), agent)
	require.NoError(t, err)
	assert.Equal(t, execution.ControllerProof{Operator: "c", Agent: agent, Epoch: "7"}, proof)
	assert.Equal(t, "/operators/c/introspection", got.path)
	assert.Equal(t, map[string]any{keyCertificatePEM: leaf, "agent": agent}, got.body)
}

func TestGaram_RefusalsAreTold(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"403 is control's authority refused", http.StatusForbidden, execution.ErrNotAuthorized},
		{"404 is control's authority refused", http.StatusNotFound, execution.ErrNotAuthorized},
		{"422 is the leaf refused", http.StatusUnprocessableEntity, execution.ErrCredentialRefused},
		{"409 is the activation superseded", http.StatusConflict, execution.ErrActivationSuperseded},
		{"503 on every attempt is undecided", http.StatusServiceUnavailable, execution.ErrUndecided},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := stand(t, garammachine.ExecutionFence, tt.status, `{"kind":"k","message":"m"}`)
			_, _, err := g.Activate(context.Background(), agent, execution.ActivationCall{RequestID: "r"})
			require.ErrorIs(t, err, tt.want)
		})
	}
	// Control: a 400 is the call's own failure, none of the refusals.
	g, _ := stand(t, garammachine.ExecutionFence, http.StatusBadRequest, `{"kind":"invalid_argument","message":"m"}`)
	_, _, err := g.Activate(context.Background(), agent, execution.ActivationCall{RequestID: "r"})
	require.Error(t, err)
	for _, refusal := range []error{execution.ErrNotAuthorized, execution.ErrCredentialRefused, execution.ErrActivationSuperseded, execution.ErrUndecided} {
		assert.NotErrorIs(t, err, refusal)
	}
}

func TestGaram_AnAnswerUnderAnotherContractOrNoneDecidesNothing(t *testing.T) {
	introspection := `{"grn":"` + agent + `","credential":"current","generation":"current","activationId":"a1"}`
	for name, header := range map[string]*string{
		"the contract asked": ptrTo(garammachine.ExecutionFence), "another contract": ptrTo("execution-fence.v2"),
		"no contract": nil,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if header != nil {
					w.Header().Set("Garam-Contract-Version", *header)
				}
				_, _ = w.Write([]byte(introspection))
			}))
			t.Cleanup(server.Close)
			_, err := executiongaram.NewGaram(garammachine.New(server.URL, server.Client())).
				Introspect(context.Background(), agent, []byte(leaf), gen)
			if header != nil && *header == garammachine.ExecutionFence {
				assert.NoError(t, err)
				return
			}
			var foreign *execution.GaramContractError
			require.ErrorAs(t, err, &foreign)
			want := ""
			if header != nil {
				want = *header
			}
			assert.Equal(t, execution.GaramContractError{Call: "/agents/" + agent + "/execution/introspection", Contract: want}, *foreign)
			assert.NotErrorIs(t, err, execution.ErrUndecided)
		})
	}
}

func ptrTo(s string) *string { return &s }
