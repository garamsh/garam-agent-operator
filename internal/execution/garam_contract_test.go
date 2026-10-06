package execution_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/execution"
)

// foreignContract is a contract the agent routes do not take; keyContract, keyLevel and keyPort are
// the log fields read.
const (
	foreignContract = "execution-fence.v2"
	keyContract     = "contract"
	keyLevel        = "level"
	keyPort         = "port"
)

// syncBuffer is a log destination the routes write from their handlers' goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// foreignContractLogs is every logged contract garam answered under that the routes do not take,
// with the port it names, in the order logged.
func (b *syncBuffer) foreignContractLogs(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var logged []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["msg"] == "garam answered under a contract the agent routes do not take" {
			logged = append(logged, map[string]any{keyLevel: record[keyLevel], keyPort: record[keyPort], keyContract: record[keyContract]})
		}
	}
	return logged
}

func TestActivate_GaramsAnswerUnderAnotherContractOrNoneIsUndecidedAndLoggedOncePerContract(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnvLogging(t, slog.New(slog.NewJSONHandler(logs, nil)))
	const call = "/agents/" + agent + "/execution/introspection"

	for _, contract := range []string{foreignContract, foreignContract, ""} {
		e.garam.set(func(g *garam) { g.introspect = &execution.GaramContractError{Call: call, Contract: contract} })
		refused := e.activate(t, e.adapter, requestID, generation, "1")
		assert.Equal(t, http.StatusServiceUnavailable, refused.status, refused.raw)
		// agent-execution.v1's only 503 kind (garam@59fe68d ADR-0084); the message names the contract.
		assert.Equal(t, kindUndecided, refused.kind())
		assert.Contains(t, refused.raw, `under contract \"`+contract+`\"`)
		assert.Equal(t, execution.Contract, refused.contract)
	}
	assert.Equal(t, []map[string]any{
		{keyLevel: "ERROR", keyPort: "execution", keyContract: foreignContract},
		{keyLevel: "ERROR", keyPort: "execution", keyContract: ""},
	}, logs.foreignContractLogs(t), "each contract value is logged once, however often it is answered")

	step := "runtime status, under the contract already logged"
	e.garam.set(func(g *garam) { g.introspect = &execution.GaramContractError{Call: call, Contract: ""} })
	reported := e.report(t, e.adapter, statusBody(firstActivation, generation, "1", "serving"))
	assert.Equal(t, http.StatusServiceUnavailable, reported.status, step)
	assert.Equal(t, kindUndecided, reported.kind(), step)
	assert.Len(t, logs.foreignContractLogs(t), 2, step)

	// Control: once garam answers under the contract, the same activation is made.
	e.garam.set(func(g *garam) { g.introspect = nil })
	activated := e.activate(t, e.adapter, requestID, generation, "1")
	assert.Equal(t, http.StatusCreated, activated.status, activated.raw)
}
