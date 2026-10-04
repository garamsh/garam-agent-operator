package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// createRequest is a create request's body, as the console sends it.
type createRequest struct {
	RequestID  string `json:"requestId"`
	Controller string `json:"controller"`
	Template   ref    `json:"template"`
	Profile    ref    `json:"profile"`
}

type ref struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

// otherController is a controller no test creates its agent on.
const otherController = "grn:acme:default:operator:other"

// createBody is a create request's body, for the env's template and profile unless changed.
func (e *env) createBody(requestID string, change func(*createRequest)) []byte {
	in := createRequest{
		RequestID:  requestID,
		Controller: controllerGRN,
		Template:   ref{Name: e.template.Name, Version: int64(e.template.Version)},
		Profile:    ref{Name: e.profile.Name, Version: int64(e.profile.Version)},
	}
	if change != nil {
		change(&in)
	}
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	return b
}

// authorizeCreate registers a new authority binding what garam would bind for a create of body.
func (e *env) authorizeCreate(requestID string, body []byte, change func(*console.Binding)) console.Authority {
	return e.authorize(requestID, body, func(b *console.Binding) {
		b.Operation, b.Target, b.Assignment = console.OperationCreate, controllerGRN, nil
		if change != nil {
			change(b)
		}
	})
}

// created is what a create request was answered.
type created struct {
	status   int
	agent    string
	revision string
	epoch    string
	message  string
}

func (e *env) create(t *testing.T, authority console.Authority, body []byte) created {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url+"/v1/orgs/"+org+"/agents", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+string(authority))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Agent    string `json:"agent"`
		Revision string `json:"revision"`
		Epoch    string `json:"epoch"`
		Message  string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return created{status: resp.StatusCode, agent: out.Agent, revision: out.Revision, epoch: out.Epoch, message: out.Message}
}

func TestCreate_CreatesTheAgentWithRevisionOneRecordedForItsAssignment(t *testing.T) {
	e := newEnv(t)
	body := e.createBody("n1", nil)

	got := e.create(t, e.authorizeCreate("n1", body, nil), body)
	require.Equal(t, http.StatusCreated, got.status, got.message)
	assert.Equal(t, "grn:acme:default:agent:n1", got.agent)
	assert.Equal(t, "1", got.revision)
	assert.Equal(t, "1", got.epoch)

	d, err := e.definitions.GetDefinition(context.Background(), definition.GRN(got.agent))
	require.NoError(t, err)
	assert.Equal(t, definition.Revision(1), d.Revision)
	assert.Equal(t, &definition.Assignment{Operator: controllerGRN, Epoch: "1"}, d.Assignment)
}

func TestCreate_IdenticalRepeatAnswersTheFirstOutcome(t *testing.T) {
	e := newEnv(t)
	body := e.createBody("n1", nil)
	first := e.create(t, e.authorizeCreate("n1", body, nil), body)
	require.Equal(t, http.StatusCreated, first.status, first.message)

	// garam mints a fresh authority for the same request id and binding when the console retries.
	repeat := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusOK, repeat.status, repeat.message)
	assert.Equal(t, first.agent, repeat.agent)
	assert.Equal(t, first.epoch, repeat.epoch)
	assert.Equal(t, 3, e.registrar.calls, "the repeat was answered without garam's recheck")
}

func TestCreate_ChangedBodyUnderTheSameRequestIDRefused(t *testing.T) {
	e := newEnv(t)
	body := e.createBody("n1", nil)
	require.Equal(t, http.StatusCreated, e.create(t, e.authorizeCreate("n1", body, nil), body).status)

	// The same request id for another controller, under an authority binding that body and controller.
	other := e.createBody("n1", func(in *createRequest) { in.Controller = otherController })
	refused := e.create(t, e.authorizeCreate("n1", other, func(b *console.Binding) {
		b.Target = otherController
	}), other)
	assert.Equal(t, http.StatusConflict, refused.status, refused.message)

	// Control: the first body under the same request id is a repeat, not a reuse.
	repeat := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusOK, repeat.status, repeat.message)
}

func TestCreate_BoundFieldMismatchRefused(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		change func(*console.Binding)
	}{
		{"an agent:configure authority", "operation", func(b *console.Binding) { b.Operation = console.OperationConfigure }},
		{"another controller", "target", func(b *console.Binding) { b.Target = otherController }},
		{"another request id", "request id", func(b *console.Binding) { b.RequestID = "other" }},
		{"another organization", "organization", func(b *console.Binding) { b.Org = "grn:root:default:org:other" }},
		{"another audience", "audience", func(b *console.Binding) { b.Audience = "grn:root:default:operator:other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body := e.createBody("n1", nil)

			refused := e.create(t, e.authorizeCreate("n1", body, tt.change), body)
			assert.Equal(t, http.StatusForbidden, refused.status, refused.message)
			assert.Contains(t, refused.message, tt.field)
			assert.Equal(t, 1, e.registrar.calls, "garam was asked to create under a refused authority")

			// Control: an authority binding every field as the request carries it creates the agent.
			accepted := e.create(t, e.authorizeCreate("n1", body, nil), body)
			assert.Equal(t, http.StatusCreated, accepted.status, accepted.message)
		})
	}
}

func TestCreate_BodyDigestMismatchRefused(t *testing.T) {
	e := newEnv(t)
	body := e.createBody("n1", nil)
	other := e.createBody("n1", func(in *createRequest) { in.Controller = otherController })

	refused := e.create(t, e.authorizeCreate("n1", other, nil), body)
	assert.Equal(t, http.StatusForbidden, refused.status, refused.message)

	// Control: an authority binding this body's digest creates the agent.
	accepted := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusCreated, accepted.status, accepted.message)
}

func TestCreate_GaramRefusalIsStoredAndAnswered(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"current authority refuses", fmt.Errorf("garam answered 403: %w", definition.ErrRegistrationRefused), http.StatusForbidden},
		{"another request holds the id", fmt.Errorf("garam answered 409: %w", definition.ErrRegistrationConflict), http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.registrar.answer(tt.err)
			body := e.createBody("n1", nil)

			refused := e.create(t, e.authorizeCreate("n1", body, nil), body)
			assert.Equal(t, tt.status, refused.status, refused.message)
			e.registrar.answer(nil)
			again := e.create(t, e.authorizeCreate("n1", body, nil), body)
			assert.Equal(t, tt.status, again.status, "a stored refusal was not answered again")
			assert.Equal(t, 2, e.registrar.calls)

			// Control: another request id is another creation, which garam now answers.
			otherBody := e.createBody("n2", nil)
			accepted := e.create(t, e.authorizeCreate("n2", otherBody, nil), otherBody)
			assert.Equal(t, http.StatusCreated, accepted.status, accepted.message)
		})
	}
}

func TestCreate_UndecidedGaramAnswers503AndTheRepeatResumes(t *testing.T) {
	e := newEnv(t)
	e.registrar.answer(fmt.Errorf("after 3 attempts: %w", definition.ErrRegistrationUndecided))
	body := e.createBody("n1", nil)

	undecided := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusServiceUnavailable, undecided.status, undecided.message)

	// Control: once garam answers, the same request is created, not refused as a reuse.
	e.registrar.answer(nil)
	resumed := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusCreated, resumed.status, resumed.message)
}

func TestCreate_RepeatAfterTheAgentMovedRefused(t *testing.T) {
	e := newEnv(t)
	body := e.createBody("n1", nil)
	require.Equal(t, http.StatusCreated, e.create(t, e.authorizeCreate("n1", body, nil), body).status)

	// Control: while the agent is where its creation put it, a repeat is answered.
	assert.Equal(t, http.StatusOK, e.create(t, e.authorizeCreate("n1", body, nil), body).status)

	e.registrar.answer(fmt.Errorf("moved: %w", definition.ErrRegistrationConflict))
	moved := e.create(t, e.authorizeCreate("n1", body, nil), body)
	assert.Equal(t, http.StatusConflict, moved.status, moved.message)
	assert.Contains(t, moved.message, "moved")
}

func TestCreate_RefusesWhatItCannotCreateFrom(t *testing.T) {
	e := newEnv(t)
	unpublished := e.createBody("n1", func(in *createRequest) { in.Profile = ref{Name: "unpublished", Version: 1} })
	assert.Equal(t, http.StatusNotFound, e.create(t, e.authorizeCreate("n1", unpublished, nil), unpublished).status)

	malformed := []byte(`{"requestId":"n2","controller":"` + controllerGRN + `","unknown":true}`)
	assert.Equal(t, http.StatusBadRequest, e.create(t, e.authorizeCreate("n2", malformed, nil), malformed).status)

	// Control: the same request id with a body naming what is published creates the agent.
	body := e.createBody("n3", nil)
	assert.Equal(t, http.StatusCreated, e.create(t, e.authorizeCreate("n3", body, nil), body).status)
}
