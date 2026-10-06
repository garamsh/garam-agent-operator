package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/console/lifecycle"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const agent = "grn:acme:default:agent:1e9ac1"

// seen is what the stand-in garam last received.
type seen struct {
	method, target, contract, authorization, body string
}

// stand is a stand-in garam answering status and answer under contract, counting its calls.
func stand(t *testing.T, contract string, status int, answer string) (*lifecycle.Garam, *seen, *int) {
	t.Helper()
	got, calls := &seen{}, new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*calls++
		*got = seen{r.Method, r.URL.RequestURI(), r.Header.Get("Garam-Contract-Version"), r.Header.Get("Authorization"), string(raw)}
		w.Header().Set("Garam-Contract-Version", contract)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return lifecycle.NewGaram(machine), got, calls
}

func TestGaram_RecoverSendsTheBodyExactlyUnderTheHandoff(t *testing.T) {
	g, got, _ := stand(t, garammachine.ExecutionFence, http.StatusCreated,
		`{"grn":"`+agent+`","lineage":"lineage-2","certificatePem":"cert"}`)
	// Not what encoding/json would write: the bytes, not their meaning, are what the handoff binds.
	body := []byte(`{"epoch":"3", "requestId":"rec-1","certificateRequestPem":"csr"}`)

	recovered, err := g.Recover(context.Background(), agent, "recover-handoff", body)
	require.NoError(t, err)
	assert.Equal(t, definition.RecoveredCredential{Lineage: "lineage-2", CertificatePEM: "cert"}, recovered)
	assert.Equal(t, seen{http.MethodPost, "/agents/" + agent + "/credential-recovery", garammachine.ExecutionFence,
		"Garam-Operation recover-handoff", string(body)}, *got)
}

func TestGaram_RecoverAnswersGaramsRefusalUndecidedAndForeignContract(t *testing.T) {
	g, _, _ := stand(t, garammachine.ExecutionFence, http.StatusConflict, `{"kind":"conflict","message":"digest"}`)
	_, err := g.Recover(context.Background(), agent, "ref", []byte(`{}`))
	var refused *console.LifecycleRefusal
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, console.LifecycleRefusal{Status: http.StatusConflict, Kind: "conflict", Message: "digest"}, *refused)

	g, _, calls := stand(t, garammachine.ExecutionFence, http.StatusServiceUnavailable, `{}`)
	_, err = g.Recover(context.Background(), agent, "ref", []byte(`{}`))
	require.ErrorIs(t, err, console.ErrLifecycleUndecided)
	assert.Equal(t, 3, *calls, "an undecided recovery was not sent again within the client's bound")

	g, _, _ = stand(t, "agent-execution.v1", http.StatusCreated, `{"grn":"`+agent+`","lineage":"l","certificatePem":"c"}`)
	_, err = g.Recover(context.Background(), agent, "ref", []byte(`{}`))
	require.ErrorIs(t, err, console.ErrGaramContractUnsupported)

	g, _, _ = stand(t, garammachine.ExecutionFence, http.StatusCreated, `{"grn":"grn:acme:default:agent:other","lineage":"l","certificatePem":"c"}`)
	_, err = g.Recover(context.Background(), agent, "ref", []byte(`{}`))
	require.Error(t, err, "a recovery of another agent was taken")
	assert.False(t, errors.As(err, &refused))
}

func TestGaram_DeactivateEndsTheActivation(t *testing.T) {
	g, got, _ := stand(t, garammachine.ExecutionFence, http.StatusOK, `{}`)
	require.NoError(t, g.Deactivate(context.Background(), agent, "activation-1"))
	assert.Equal(t, seen{http.MethodPost, "/agents/" + agent + "/activations/activation-1/deactivation",
		garammachine.ExecutionFence, "", ""}, *got)

	g, _, _ = stand(t, garammachine.ExecutionFence, http.StatusForbidden, `{"kind":"permission_denied","message":"no"}`)
	err := g.Deactivate(context.Background(), agent, "activation-1")
	var refused *console.LifecycleRefusal
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, http.StatusForbidden, refused.Status)
}
