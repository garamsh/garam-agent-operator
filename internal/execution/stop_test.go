package execution_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

func TestActivate_NothingIsActivatedWhileAStopHoldsTheAgent(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	first := e.activate(t, e.adapter, requestID, generation, "1")
	require.Equal(t, http.StatusCreated, first.status, first.raw)
	stop := definition.StopInput{
		Key: definition.RequestKey{Organization: "acme", RequestID: "stop-1"}, Agent: agent,
		Binding: definition.Binding{Actor: "admin", Operation: "agent:configure", Target: agent},
	}
	_, _, err := e.definitions.Stop(ctx, stop)
	require.NoError(t, err)

	for _, id := range []string{requestID, "e5b1c8f0-0d55-4f2c-9a3e-6f1d2c7b8a90"} {
		refused := e.activate(t, e.adapter, id, generation, "1")
		assert.Equal(t, http.StatusForbidden, refused.status, refused.raw)
		assert.Equal(t, "placement_not_current", refused.kind(), "a stopped agent's activation is not refused under a contract kind")
	}
	assert.Equal(t, 1, e.garam.callCount(), "garam was asked to activate a stopped agent")

	// Control: once the stop ends, the adapter's activation is admitted again.
	_, err = e.definitions.Start(ctx, definition.StopInput{
		Key: definition.RequestKey{Organization: "acme", RequestID: "start-1"}, Agent: agent,
		Binding: definition.Binding{Actor: "admin", Operation: "agent:configure", Target: agent},
	})
	require.NoError(t, err)
	again := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusOK, again.status, again.raw)
}
