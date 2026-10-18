package registrar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/registrar"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

const controller = "grn:acme:default:operator:k8s"

// operationRef is the creation's reference every registration here sends.
const operationRef = "ref-1"

// atPrepare names the prepare stage in the cases that refuse at either stage.
const atPrepare = "prepare"

// approvalID is the approval the stand-in garam prepares.
const approvalID = "7f0c2a4e-1b3d-4c5e-8f60-718293a4b5c6"

const (
	approved = `{"approvalId":"` + approvalID + `","requestId":"n1","placement":"managed","assignee":"` +
		controller + `","operationRef":"ref-1"}`
	admitted = `{"grn":"grn:acme:default:agent:0a1b","placement":"managed","assignee":"` + controller +
		`","epoch":"1","lineage":"0f1e2d3c-4b5a-4968-8776-655443322110","mode":"fenced","credentialDelivery":"mintAgentToken"}`
)

// answer is one answer the stand-in garam gives.
type answer struct {
	status int
	body   string
}

// sent is one request the stand-in garam received.
type sent struct {
	path     string
	contract string
	body     map[string]string
}

// garam stands in for garam's two agent-enrollment.v1 stages. Each stage gives its answers in
// order, repeating the last, under contract.
type garam struct {
	prepare, admit []answer
	contract       string

	mu       sync.Mutex
	received []sent
	prepared int
	admitted int
}

func (g *garam) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	got := sent{path: r.URL.EscapedPath(), contract: r.Header.Get("Garam-Contract-Version")}
	_ = json.NewDecoder(r.Body).Decode(&got.body)
	g.received = append(g.received, got)
	var a answer
	if strings.HasSuffix(got.path, "/admission") {
		a = g.admit[min(g.admitted, len(g.admit)-1)]
		g.admitted++
	} else {
		a = g.prepare[min(g.prepared, len(g.prepare)-1)]
		g.prepared++
	}
	w.Header().Set("Garam-Contract-Version", g.contract)
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.body))
}

// requests is every request garam received, in order.
func (g *garam) requests() []sent {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]sent(nil), g.received...)
}

// paths is every path garam received, in order.
func (g *garam) paths() []string {
	requests := g.requests()
	paths := make([]string, 0, len(requests))
	for _, s := range requests {
		paths = append(paths, s.path)
	}
	return paths
}

// newGaram answers both stages as a first admission does, under agent-enrollment.v1.
func newGaram() *garam {
	return &garam{
		prepare:  []answer{{http.StatusCreated, approved}},
		admit:    []answer{{http.StatusCreated, admitted}},
		contract: garammachine.AgentEnrollment,
	}
}

// registrarFor is a registrar calling g.
func registrarFor(t *testing.T, g *garam) *registrar.Garam {
	t.Helper()
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	machine := garammachine.New(server.URL, server.Client())
	machine.Backoff = time.Millisecond
	return registrar.NewGaram(machine)
}

func register(r *registrar.Garam) (definition.Registered, error) {
	return r.Register(context.Background(), definition.Registration{
		Request:      definition.RequestKey{Organization: "acme", RequestID: "n1"},
		Controller:   controller,
		OperationRef: operationRef,
	})
}

const (
	preparePath = "/agent-enrollment-approvals"
	admitPath   = "/agent-enrollment-approvals/" + approvalID + "/admission"
)

var theAgent = definition.Registered{Agent: "grn:acme:default:agent:0a1b", Epoch: "1"}

func TestGaram_PreparesThenAdmitsTheManagedAgent(t *testing.T) {
	g := newGaram()
	r, err := register(registrarFor(t, g))
	require.NoError(t, err)
	assert.Equal(t, theAgent, r)

	binding := map[string]string{
		"requestId": "n1", "placement": "managed", "assignee": controller, "operationRef": "ref-1",
	}
	assert.Equal(t, []sent{
		{path: preparePath, contract: garammachine.AgentEnrollment, body: binding},
		{path: admitPath, contract: garammachine.AgentEnrollment, body: binding},
	}, g.requests())
}

func TestGaram_ARepeatAnswersTheSameAgent(t *testing.T) {
	g := newGaram()
	g.prepare = []answer{{http.StatusCreated, approved}, {http.StatusOK, approved}}
	g.admit = []answer{{http.StatusCreated, admitted}, {http.StatusOK, admitted}}
	r := registrarFor(t, g)

	first, err := register(r)
	require.NoError(t, err)
	again, err := register(r)
	require.NoError(t, err)
	assert.Equal(t, theAgent, first)
	assert.Equal(t, theAgent, again)
}

func TestGaram_ACrashBetweenTheStagesRecoversByPreparingAgain(t *testing.T) {
	g := newGaram()
	g.prepare = []answer{{http.StatusCreated, approved}, {http.StatusOK, approved}}
	// The first admission is never decided: every attempt the machine client sends answers 503.
	g.admit = []answer{
		{http.StatusServiceUnavailable, ""}, {http.StatusServiceUnavailable, ""}, {http.StatusServiceUnavailable, ""},
		{http.StatusCreated, admitted},
	}
	r := registrarFor(t, g)

	_, err := register(r)
	require.ErrorIs(t, err, definition.ErrRegistrationUndecided)
	again, err := register(r)
	require.NoError(t, err)
	assert.Equal(t, theAgent, again)
	assert.Equal(t, []string{preparePath, admitPath, admitPath, admitPath, preparePath, admitPath}, g.paths())
}

func TestGaram_AReferenceExpiredBeforeTheRetryIsRefused(t *testing.T) {
	undecided := []answer{
		{http.StatusServiceUnavailable, ""}, {http.StatusServiceUnavailable, ""}, {http.StatusServiceUnavailable, ""},
		{http.StatusCreated, admitted},
	}
	expired := answer{http.StatusForbidden, `{"kind":"permission_denied","message":"operation reference is not authorized"}`}

	t.Run("at prepare", func(t *testing.T) {
		g := newGaram()
		g.prepare = []answer{{http.StatusCreated, approved}, expired}
		g.admit = undecided
		r := registrarFor(t, g)
		_, err := register(r)
		require.ErrorIs(t, err, definition.ErrRegistrationUndecided)

		_, err = register(r)
		require.ErrorIs(t, err, definition.ErrRegistrationRefused)
		assert.Equal(t, []string{preparePath, admitPath, admitPath, admitPath, preparePath}, g.paths(),
			"the refusal is answered once, and admission is not asked")
	})
	t.Run("at admission", func(t *testing.T) {
		g := newGaram()
		g.prepare = []answer{{http.StatusCreated, approved}, {http.StatusOK, approved}}
		g.admit = append(undecided[:3:3], expired)
		r := registrarFor(t, g)
		_, err := register(r)
		require.ErrorIs(t, err, definition.ErrRegistrationUndecided)

		_, err = register(r)
		require.ErrorIs(t, err, definition.ErrRegistrationRefused)
		assert.Equal(t, []string{preparePath, admitPath, admitPath, admitPath, preparePath, admitPath}, g.paths())
	})
	// Control: the same retry, where garam still authorizes the reference, admits the agent.
	g := newGaram()
	g.prepare = []answer{{http.StatusCreated, approved}, {http.StatusOK, approved}}
	g.admit = undecided
	r := registrarFor(t, g)
	_, err := register(r)
	require.ErrorIs(t, err, definition.ErrRegistrationUndecided)
	again, err := register(r)
	require.NoError(t, err)
	assert.Equal(t, theAgent, again)
}

func TestGaram_RefusalsAreTold(t *testing.T) {
	tests := []struct {
		name   string
		answer answer
		want   error
		reason string
	}{
		{"403 is a refusal", answer{http.StatusForbidden, `{"kind":"permission_denied","message":"refused"}`},
			definition.ErrRegistrationRefused, ""},
		{"404 is a refusal", answer{http.StatusNotFound, `{"kind":"not_found","message":"unknown"}`},
			definition.ErrRegistrationRefused, ""},
		{"409 is a conflict", answer{http.StatusConflict,
			`{"kind":"conflict","message":"another binding","reason":"binding_conflict"}`},
			definition.ErrRegistrationConflict, "binding_conflict"},
		{"503 on every attempt is undecided", answer{http.StatusServiceUnavailable, ""},
			definition.ErrRegistrationUndecided, ""},
	}
	for _, stage := range []string{atPrepare, "admission"} {
		for _, tt := range tests {
			t.Run(stage+": "+tt.name, func(t *testing.T) {
				g := newGaram()
				if stage == atPrepare {
					g.prepare = []answer{tt.answer}
				} else {
					g.admit = []answer{tt.answer}
				}
				_, err := register(registrarFor(t, g))
				require.ErrorIs(t, err, tt.want)
				if tt.reason != "" {
					assert.Contains(t, err.Error(), "reason "+tt.reason)
				}
			})
		}
	}
	// Control: a 400, and a 401 refusing this service's own token, are the call's own failure at
	// either stage, neither a refusal nor undecided, so a creation stays Pending.
	for _, stage := range []string{atPrepare, "admission"} {
		for _, own := range []answer{
			{http.StatusBadRequest, `{"kind":"invalid_argument","message":"malformed"}`},
			{http.StatusUnauthorized, `{"kind":"unauthenticated","message":"operator token is not valid"}`},
		} {
			g := newGaram()
			if stage == atPrepare {
				g.prepare = []answer{own}
			} else {
				g.admit = []answer{own}
			}
			_, err := register(registrarFor(t, g))
			require.Error(t, err, "%s answered %d", stage, own.status)
			for _, refusal := range []error{definition.ErrRegistrationRefused, definition.ErrRegistrationConflict, definition.ErrRegistrationUndecided} {
				assert.NotErrorIs(t, err, refusal, "%s answered %d", stage, own.status)
			}
		}
	}
}

func TestGaram_AnswerWithoutAnApprovalOrAnAgentRefused(t *testing.T) {
	for name, set := range map[string]func(g *garam){
		"no approval": func(g *garam) { g.prepare = []answer{{http.StatusCreated, `{"approvalId":""}`}} },
		"no agent":    func(g *garam) { g.admit = []answer{{http.StatusCreated, `{"grn":"","epoch":"1"}`}} },
		"no epoch": func(g *garam) {
			g.admit = []answer{{http.StatusCreated, `{"grn":"grn:acme:default:agent:0a1b","epoch":""}`}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGaram()
			set(g)
			_, err := register(registrarFor(t, g))
			require.Error(t, err)
		})
	}
	// Control: answers naming an approval, then an agent and an epoch, are read.
	_, err := register(registrarFor(t, newGaram()))
	require.NoError(t, err)
}

func TestGaram_AnAnswerUnderAnotherContractOrNoneDecidesNothing(t *testing.T) {
	for name, contract := range map[string]string{
		"the contract asked": garammachine.AgentEnrollment, "another contract": "agent-enrollment.v2", "no contract": "",
	} {
		t.Run(name, func(t *testing.T) {
			g := newGaram()
			g.contract = contract
			_, err := register(registrarFor(t, g))
			if contract == garammachine.AgentEnrollment {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, definition.ErrGaramContractUnsupported)
			assert.NotErrorIs(t, err, definition.ErrRegistrationRefused, "a refusal would fail the creation")
		})
	}
}
