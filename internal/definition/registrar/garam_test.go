package registrar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/registrar"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const controller = "grn:acme:default:operator:k8s"

// sent is what the stand-in garam last received.
type sent struct {
	path     string
	contract string
	body     map[string]string
}

func register(t *testing.T, status int, answer string) (definition.Registered, sent, error) {
	t.Helper()
	var got sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.contract = r.URL.EscapedPath(), r.Header.Get("Garam-Contract-Version")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Garam-Contract-Version", garammachine.ManagedEnrollment)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	r, err := registrar.NewGaram(machine).Register(context.Background(), definition.Registration{
		Request:      definition.RequestKey{Organization: "acme", RequestID: "n1"},
		Controller:   controller,
		OperationRef: "ref-1",
	})
	return r, got, err
}

func TestGaram_CreatesTheManagedAgent(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r, got, err := register(t, status, `{"grn":"grn:acme:default:agent:0a1b","epoch":"1"}`)
			require.NoError(t, err)
			assert.Equal(t, definition.Registered{Agent: "grn:acme:default:agent:0a1b", Epoch: "1"}, r)
			assert.Equal(t, "/operators/"+controller+"/managed-agents", got.path)
			assert.Equal(t, garammachine.ManagedEnrollment, got.contract)
			assert.Equal(t, map[string]string{"requestId": "n1", "operationRef": "ref-1"}, got.body)
		})
	}
}

func TestGaram_RefusalsAreTold(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusForbidden, definition.ErrRegistrationRefused},
		{http.StatusNotFound, definition.ErrRegistrationRefused},
		{http.StatusConflict, definition.ErrRegistrationConflict},
		{http.StatusServiceUnavailable, definition.ErrRegistrationUndecided},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			_, _, err := register(t, tt.status, `{"message":"refused"}`)
			require.ErrorIs(t, err, tt.want)
		})
	}
	// Control: a 400 is the call's own failure, neither a refusal nor undecided.
	_, _, err := register(t, http.StatusBadRequest, `{"message":"malformed"}`)
	require.Error(t, err)
	for _, refusal := range []error{definition.ErrRegistrationRefused, definition.ErrRegistrationConflict, definition.ErrRegistrationUndecided} {
		assert.NotErrorIs(t, err, refusal)
	}
}

func TestGaram_AnswerWithoutAnAgentRefused(t *testing.T) {
	_, _, err := register(t, http.StatusCreated, `{"grn":"","epoch":"1"}`)
	require.Error(t, err)
	_, _, err = register(t, http.StatusCreated, `{"grn":"grn:acme:default:agent:0a1b","epoch":""}`)
	require.Error(t, err)

	// Control: the same answer naming both an agent and an epoch is read.
	_, _, err = register(t, http.StatusCreated, `{"grn":"grn:acme:default:agent:0a1b","epoch":"1"}`)
	require.NoError(t, err)
}
