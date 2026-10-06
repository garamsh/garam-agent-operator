package console_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/garam-agent-operator/internal/console"
	garamintrospector "github.com/garamsh/garam-agent-operator/internal/console/introspector"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// kindGaramContractUnsupported is the kind a garam answer under a contract this service does not
// take, or under none, is refused under.
const kindGaramContractUnsupported = "garam_contract_unsupported"

// contractCase is how a garam double answers, and what the route then answers.
type contractCase struct {
	name     string
	contract string
	// omit sends no Garam-Contract-Version header at all.
	omit   bool
	status int
	kind   string
}

// contractCases are the accepted control, an answer under another contract, and one under none.
var contractCases = []contractCase{
	{name: "the contract asked", contract: garammachine.OperationAuthority, status: http.StatusOK},
	{name: "another contract", contract: "operation-authority.v2", status: http.StatusServiceUnavailable,
		kind: kindGaramContractUnsupported},
	{name: "no contract", omit: true, status: http.StatusServiceUnavailable, kind: kindGaramContractUnsupported},
}

// introspectionGaram stands in for garam's introspection: it answers binding under contract, or
// under no contract header where omit.
func introspectionGaram(t *testing.T, binding console.Binding, contract string, omit bool) *garammachine.Client {
	t.Helper()
	wire := map[string]any{
		"operationRef": binding.OperationRef, "grantId": binding.GrantID, "orgGrn": binding.Org,
		"actorGrn": binding.Actor, "audienceGrn": binding.Audience, "operation": binding.Operation,
		"targetGrn": binding.Target, "requestId": binding.RequestID, "bodySha256": binding.BodySHA256,
		"requestTarget": binding.RequestTarget, "expiresAt": binding.ExpiresAt.Format(time.RFC3339),
	}
	if binding.Assignment != nil {
		wire["assignment"] = map[string]string{"operatorGrn": binding.Assignment.Operator, "epoch": binding.Assignment.Epoch}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !omit {
			w.Header().Set("Garam-Contract-Version", contract)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wire)
	}))
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return machine
}

func TestConfigure_GaramsAnswerUnderAnotherContractOrNoneIsRefused503(t *testing.T) {
	for _, tc := range contractCases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			body := e.body("c1", "edited", 1)
			authority := e.authorize("c1", body, nil)
			e.introspector.mu.Lock()
			binding := e.introspector.answers[authority].binding
			e.introspector.mu.Unlock()

			// The console over garam's real introspection port, against the double.
			server := httptest.NewServer(console.NewHandler(console.Config{
				Definitions:  e.definitions,
				Introspector: garamintrospector.NewGaram(introspectionGaram(t, binding, tc.contract, tc.omit)),
				Audience:     audience,
				Now:          func() time.Time { return now },
				Logger:       slog.New(slog.DiscardHandler),
			}))
			t.Cleanup(server.Close)
			e.url = server.URL

			got := e.configure(t, authority, body)
			assert.Equal(t, tc.status, got.status, got.message)
			assert.Equal(t, tc.kind, got.kind)
			want := definition.Revision(1)
			if tc.status == http.StatusOK {
				want = 2
			}
			assert.Equal(t, want, e.revision(t).Revision, "what the answer authorized")
		})
	}
}

func TestCreate_GaramsManagedCreateUnderAnotherContractIsRefused503AndLeavesTheCreationPending(t *testing.T) {
	e := newEnv(t)
	e.registrar.answer(fmt.Errorf("create managed agent: %w", definition.ErrGaramContractUnsupported))
	body := e.createBody("n1", nil)

	got := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusServiceUnavailable, got.status, got.message)
	assert.Equal(t, kindGaramContractUnsupported, got.kind)

	// Control: the same create, once garam answers under the contract, creates the agent.
	e.registrar.answer(nil)
	again := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusCreated, again.status, again.message)
}
