package console_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/console"
	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

const (
	// The attempt stages, the dispositions and the keys these tests use.
	attemptFrozen   = "frozen"
	attemptSwitched = "switched"
	dispArchive     = "archive"
	dispImport      = "import"
	keyPin          = "tools.pins.files"
	keyModel        = "model.name"
	pinValue        = "sha256:files"
	keyNote         = "team-note"
	kindNotEligible = "not_cut_over_eligible"

	legacy       = "grn:acme:default:agent:1e9ac1"
	legacyEpoch  = "3"
	importID     = "import-1"
	configureRef = "configure-ref"
)

// call is one stage garam was asked, with the reference it carried.
type call struct {
	stage console.Stage
	ref   string
}

// cutover is the test double for garam's cutover routes, holding one legacy agent's source and
// its attempts as agent-cutover.v1 does: a freeze is verified against the source's digest, a
// switch needs a frozen attempt, and a switched one cannot be rolled back.
type cutover struct {
	mu       sync.Mutex
	values   map[string]string
	mode     string
	class    string
	digest   string
	attempts map[string]string
	refuse   map[console.Stage]error
	calls    []call
}

func sourceDigestOf(t *testing.T, values map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"agentGrn": legacy, "operatorGrn": controllerGRN, "values": values})
	require.NoError(t, err)
	canonical, err := jcs.Transform(raw)
	require.NoError(t, err)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func (c *cutover) Target(agent string, stage console.Stage) string {
	suffix := map[console.Stage]string{console.StageRead: "", console.StageFreeze: "/freeze",
		console.StageSwitch: "/switch", console.StageRollback: "/rollback"}[stage]
	return "/agents/" + url.PathEscape(agent) + "/cutover" + suffix
}

func (c *cutover) record(stage console.Stage, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call{stage, ref})
	return c.refuse[stage]
}

func (c *cutover) Read(_ context.Context, agent, ref string) (console.CutoverSource, error) {
	if err := c.record(console.StageRead, ref); err != nil {
		return console.CutoverSource{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return console.CutoverSource{Agent: agent, Assignee: controllerGRN, Epoch: legacyEpoch, Mode: c.mode, Class: c.class,
		HasSource: true, Operator: controllerGRN, Values: c.values, SourceDigest: c.digest}, nil
}

func (c *cutover) Freeze(_ context.Context, _, ref, id, digest, epoch string) (console.CutoverAttempt, error) {
	if err := c.record(console.StageFreeze, ref); err != nil {
		return console.CutoverAttempt{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if digest != c.digest || epoch != legacyEpoch {
		return console.CutoverAttempt{}, &console.CutoverRefusal{Status: http.StatusConflict, Kind: "digest_mismatch"}
	}
	c.attempts[id] = attemptFrozen
	return console.CutoverAttempt{ImportID: id, Stage: attemptFrozen, FrozenDigest: digest}, nil
}

func (c *cutover) Switch(_ context.Context, _, ref, id string) (console.CutoverAttempt, error) {
	if err := c.record(console.StageSwitch, ref); err != nil {
		return console.CutoverAttempt{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if stage := c.attempts[id]; stage != attemptFrozen && stage != attemptSwitched {
		return console.CutoverAttempt{}, &console.CutoverRefusal{Status: http.StatusNotFound, Kind: "not_found"}
	}
	c.attempts[id] = attemptSwitched
	return console.CutoverAttempt{ImportID: id, Stage: attemptSwitched}, nil
}

func (c *cutover) RollBack(_ context.Context, _, ref, id string) (console.CutoverAttempt, error) {
	if err := c.record(console.StageRollback, ref); err != nil {
		return console.CutoverAttempt{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// As garam's agent-cutover.v1 does: no rollback reaches an ended attempt, and none a switched one.
	switch c.attempts[id] {
	case attemptSwitched:
		return console.CutoverAttempt{}, &console.CutoverRefusal{Status: http.StatusConflict,
			Kind: "reverse_migration_required", Reason: "reverse_migration_required"}
	case attemptRolledBack:
		return console.CutoverAttempt{}, &console.CutoverRefusal{Status: http.StatusConflict,
			Kind: "attempt_ended", Reason: "attempt_ended", Message: "the cutover attempt has been rolled back"}
	}
	c.attempts[id] = attemptRolledBack
	return console.CutoverAttempt{ImportID: id, Stage: "rolled_back"}, nil
}

// callsTo is how many times garam was asked stage.
func (c *cutover) callsTo(stage console.Stage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, made := range c.calls {
		if made.stage == stage {
			n++
		}
	}
	return n
}

// cutoverEnv is the console's routes over an in-memory store, with garam's cutover routes stood in
// for by a double holding one legacy agent's source.
type cutoverEnv struct {
	url          string
	introspector *introspector
	cutover      *cutover
	definitions  definition.Service
	profile      definition.ProfileRef
	authorities  int
}

func newCutoverEnv(t *testing.T, values map[string]string) *cutoverEnv {
	t.Helper()
	definitions := definition.NewService(repository.NewMemory(), &registrar{}, nil)
	p, err := definitions.PublishProfile(context.Background(), org, "small", definition.ExecutionSettings{})
	require.NoError(t, err)
	e := &cutoverEnv{
		introspector: &introspector{answers: map[console.Authority]answer{}},
		cutover: &cutover{values: values, mode: "legacy", class: "eligible", digest: sourceDigestOf(t, values),
			attempts: map[string]string{}, refuse: map[console.Stage]error{}},
		definitions: definitions,
		profile:     definition.ProfileRef{Name: p.Name, Version: p.Version},
	}
	server := httptest.NewServer(console.NewHandler(console.Config{
		Definitions: definitions, Introspector: e.introspector, Cutover: e.cutover,
		Audience: audience, Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler),
	}))
	t.Cleanup(server.Close)
	e.url = server.URL
	return e
}

// authority registers an authority garam would mint for operation on the legacy agent, bound to
// requestTarget, for body under requestID, and returns it with its durable reference.
func (e *cutoverEnv) authority(operation, requestTarget, requestID string, body []byte, ref string) console.Authority {
	e.authorities++
	a := console.Authority(fmt.Sprintf("authority-%d", e.authorities))
	digest := sha256.Sum256(body)
	e.introspector.set(a, console.Binding{
		OperationRef: ref, Org: "grn:root:default:org:" + org, Actor: actor, Audience: audience,
		Operation: operation, Target: legacy, Assignment: &console.Assignment{Operator: controllerGRN, Epoch: legacyEpoch},
		RequestID: requestID, BodySHA256: hex.EncodeToString(digest[:]), RequestTarget: requestTarget,
		ExpiresAt: now.Add(5 * time.Minute),
	}, nil)
	return a
}

// stageAnswer is one stage's answer.
type stageAnswer struct {
	status int
	body   map[string]any
	raw    string
}

func (a stageAnswer) kind() string {
	kind, _ := a.body["kind"].(string)
	return kind
}

// post sends body to stage's route, carrying authority and, for the switch, configure.
func (e *cutoverEnv) post(t *testing.T, stage string, body []byte, authority, configure console.Authority) stageAnswer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url+"/v1/orgs/"+org+"/agents/"+legacy+"/cutover/"+stage, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+string(authority))
	if configure != "" {
		req.Header.Set("Garam-Configure-Operation", "Garam-Operation "+string(configure))
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := stageAnswer{status: resp.StatusCode, body: map[string]any{}, raw: string(raw)}
	require.NoError(t, json.Unmarshal(raw, &out.body), string(raw))
	return out
}

// importBody is an import of the legacy agent under the env's profile, with dispositions.
func (e *cutoverEnv) importBody(requestID string, dispositions map[string]string) []byte {
	b, _ := json.Marshal(struct {
		RequestID string `json:"requestId"`
		Profile   struct {
			Name    string `json:"name"`
			Version int64  `json:"version"`
		} `json:"profile"`
		Dispositions map[string]string `json:"dispositions"`
	}{RequestID: requestID, Profile: struct {
		Name    string `json:"name"`
		Version int64  `json:"version"`
	}{e.profile.Name, int64(e.profile.Version)}, Dispositions: dispositions})
	return b
}

func stageBody(requestID string) []byte {
	b, _ := json.Marshal(struct {
		RequestID string `json:"requestId"`
	}{requestID})
	return b
}

// switchBody is a switch under requestID, naming its configure authority's request id.
func switchBody(requestID string) []byte {
	b, _ := json.Marshal(struct {
		RequestID          string `json:"requestId"`
		ConfigureRequestID string `json:"configureRequestId"`
	}{requestID, "configure-" + requestID})
	return b
}

// doImport imports the agent, as garam's read stage authorizes it.
func (e *cutoverEnv) doImport(t *testing.T, requestID string, dispositions map[string]string) stageAnswer {
	t.Helper()
	body := e.importBody(requestID, dispositions)
	return e.post(t, "import", body,
		e.authority(console.OperationCutover, e.cutover.Target(legacy, console.StageRead), requestID, body, "read-ref"), "")
}

// doStage runs the freeze, the switch or the rollback under an authority bound to its own route.
func (e *cutoverEnv) doStage(t *testing.T, stage console.Stage, requestID string) stageAnswer {
	t.Helper()
	body := stageBody(requestID)
	if stage == console.StageSwitch {
		body = switchBody(requestID)
	}
	authority := e.authority(console.OperationCutover, e.cutover.Target(legacy, stage), requestID, body, string(stage)+"-ref")
	var configure console.Authority
	if stage == console.StageSwitch {
		configure = e.authority(console.OperationConfigure, "", "configure-"+requestID, body, configureRef)
	}
	return e.post(t, string(stage), body, authority, configure)
}

// released is the legacy agent's revision in its controller's candidate set, if any.
func (e *cutoverEnv) released(t *testing.T) (definition.DesiredRevision, bool) {
	t.Helper()
	page, err := e.definitions.Desired(context.Background(), controllerGRN, 10)
	require.NoError(t, err)
	for _, r := range page.Revisions {
		if r.Definition.Agent == legacy {
			return r, true
		}
	}
	return definition.DesiredRevision{}, false
}

func TestCutover_ImportFreezeSwitchReleasesRevisionOneOnlyOnTheSwitch(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{keyPin: pinValue, keyModel: "x", keyNote: "y"})
	dispositions := map[string]string{keyPin: dispImport, keyModel: dispArchive}

	imported := e.doImport(t, importID, dispositions)
	require.Equal(t, http.StatusCreated, imported.status, imported.raw)
	assert.Equal(t, "imported", imported.body["stage"])
	assert.Equal(t, map[string]any{keyPin: dispImport, keyModel: dispArchive, keyNote: "never-applied"},
		imported.body["dispositions"])
	d, err := e.definitions.GetDefinition(context.Background(), legacy)
	require.NoError(t, err)
	assert.Equal(t, definition.ToolPins{"files": pinValue}, d.Config.Tools)
	assert.Nil(t, d.Assignment, "the imported revision was recorded for a controller")
	_, ok := e.released(t)
	assert.False(t, ok, "the import was released before the switch")
	assert.Equal(t, http.StatusOK, e.doImport(t, importID, dispositions).status, "an identical import was not answered")

	frozen := e.doStage(t, console.StageFreeze, "freeze-1")
	require.Equal(t, http.StatusOK, frozen.status, frozen.raw)
	_, ok = e.released(t)
	assert.False(t, ok, "the import was released before the switch")

	switched := e.doStage(t, console.StageSwitch, "switch-1")
	require.Equal(t, http.StatusOK, switched.status, switched.raw)
	assert.Equal(t, "switched", switched.body["stage"])
	released, ok := e.released(t)
	require.True(t, ok, "the switched import was not released")
	assert.True(t, released.Cutover)
	assert.Equal(t, &definition.Assignment{Operator: controllerGRN, Epoch: legacyEpoch}, released.Definition.Assignment)

	// Revision 1's first activation is sent under the switch's agent:configure reference, never null.
	activation, err := e.definitions.PrepareActivation(context.Background(), legacy, definition.ActivationRequest{
		RequestID: "a1", Epoch: legacyEpoch, Generation: strings.Repeat("a", 32), ConfigRevision: 1, PlacementPodUID: "pod",
	})
	require.NoError(t, err)
	assert.Equal(t, configureRef, activation.OperationRef)

	// Each stage was carried to garam under its own reference.
	assert.Equal(t, []call{{console.StageRead, "read-ref"}, {console.StageRead, "read-ref"},
		{console.StageFreeze, "freeze-ref"}, {console.StageSwitch, "switch-ref"}}, e.cutover.calls)
}

func TestCutover_AReferenceForAnotherStageRefused(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)

	body := switchBody("switch-1")
	freezeAuthority := e.authority(console.OperationCutover, e.cutover.Target(legacy, console.StageFreeze), "switch-1", body, "freeze-ref")
	configure := e.authority(console.OperationConfigure, "", "configure-switch-1", body, configureRef)
	refused := e.post(t, "switch", body, freezeAuthority, configure)
	assert.Equal(t, http.StatusForbidden, refused.status, refused.raw)
	assert.Contains(t, refused.raw, "request target")
	assert.Equal(t, 0, e.cutover.callsTo(console.StageSwitch), "garam was asked under another stage's reference")

	// Control: the switch's own reference is carried.
	assert.Equal(t, http.StatusOK, e.doStage(t, console.StageSwitch, "switch-1").status)
}

func TestCutover_EachStageNeedsAnAgentCutoverAuthority(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	body := e.importBody(importID, nil)
	configure := e.authority(console.OperationConfigure, e.cutover.Target(legacy, console.StageRead), importID, body, "ref")
	refused := e.post(t, "import", body, configure, "")
	assert.Equal(t, http.StatusForbidden, refused.status, refused.raw)
	assert.Contains(t, refused.raw, "operation")
	assert.Equal(t, 0, e.cutover.callsTo(console.StageRead))

	// Control: an agent:cutover authority for the read imports.
	assert.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
}

func TestCutover_ImportRefusesWhatCannotBeImportedAsIs(t *testing.T) {
	values := map[string]string{keyPin: pinValue, keyModel: "x", keyNote: "y"}
	ok := map[string]string{keyPin: dispImport, keyModel: dispArchive}
	tests := []struct {
		name         string
		setup        func(c *cutover)
		dispositions map[string]string
		status       int
		kind         string
	}{
		{"a digest other than the source's", func(c *cutover) { c.digest = strings.Repeat("0", 64) }, ok,
			http.StatusConflict, "digest_mismatch"},
		{"an agent already fenced", func(c *cutover) { c.mode, c.class = "fenced", "not-applicable" }, ok,
			http.StatusUnprocessableEntity, kindNotEligible},
		{"a mode other than legacy", func(c *cutover) { c.mode = "fenced" }, ok,
			http.StatusUnprocessableEntity, kindNotEligible},
		{"values garam cannot read", func(c *cutover) { c.class = "blocked:unparseable" }, ok,
			http.StatusUnprocessableEntity, kindNotEligible},
		{"a pin with no disposition", nil, map[string]string{keyModel: dispArchive},
			http.StatusUnprocessableEntity, "key_disposition_required"},
		{"another key with no disposition", nil, map[string]string{keyPin: dispImport},
			http.StatusUnprocessableEntity, "key_disposition_required"},
		{"a key other than a pin imported", nil, map[string]string{keyPin: dispImport, keyModel: dispImport},
			http.StatusBadRequest, ""},
		{"a disposition for a value the source does not hold", nil,
			map[string]string{keyPin: dispImport, keyModel: dispArchive, "absent": dispArchive}, http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCutoverEnv(t, values)
			if tt.setup != nil {
				tt.setup(e.cutover)
			}
			refused := e.doImport(t, importID, tt.dispositions)
			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.kind, refused.kind())
			_, err := e.definitions.GetDefinition(context.Background(), legacy)
			require.ErrorIs(t, err, definition.ErrNotFound, "a refused import was stored")

			// Control: the same source, readable and verified, with every key disposed of, is imported.
			fresh := newCutoverEnv(t, values)
			assert.Equal(t, http.StatusCreated, fresh.doImport(t, importID, ok).status)
		})
	}
}

func TestCutover_AnotherImportWhileOneIsOpenRefused(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)

	refused := e.doImport(t, "import-2", nil)
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, "import_open", refused.kind())

	// Control: the open import's own request is answered.
	assert.Equal(t, http.StatusOK, e.doImport(t, importID, nil).status)
}

func TestCutover_AnAgentAlreadyDefinedHereIsNotImported(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	ctx := context.Background()
	tmpl, err := e.definitions.PublishTemplate(ctx, org, definition.Template{Name: "researcher", Profile: e.profile})
	require.NoError(t, err)
	// The registrar double names the agent for the request id, so this creation defines the
	// legacy agent's GRN here.
	_, _, err = e.definitions.CreateAgent(ctx, definition.CreateInput{
		Request:    definition.RequestKey{Organization: org, RequestID: "1e9ac1"},
		Binding:    definition.Binding{Actor: actor, Operation: console.OperationCreate, Target: controllerGRN},
		Controller: controllerGRN, Template: definition.TemplateRef{Name: tmpl.Name, Version: tmpl.Version}, Profile: e.profile,
	})
	require.NoError(t, err)
	_, err = e.definitions.GetDefinition(ctx, legacy)
	require.NoError(t, err)

	refused := e.doImport(t, importID, nil)
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, "already_defined", refused.kind())

	// Control: an agent with no definition here is imported.
	assert.Equal(t, http.StatusCreated, newCutoverEnv(t, map[string]string{}).doImport(t, importID, nil).status)
}

func TestCutover_NothingIsAddedToAnImportBeforeItsSwitch(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
	configure := func() error {
		_, err := e.definitions.Configure(context.Background(), definition.ConfigureInput{
			Request: definition.RequestKey{Organization: org, RequestID: "c1"},
			Binding: definition.Binding{Actor: actor, Operation: console.OperationConfigure, Target: legacy,
				Assignment: definition.Assignment{Operator: controllerGRN, Epoch: legacyEpoch}},
			Agent: legacy, ExpectedRevision: 1, Profile: e.profile,
		})
		return err
	}
	require.ErrorIs(t, configure(), definition.ErrCutoverPending)

	// Control: once switched, revision 2 may follow.
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageSwitch, "switch-1").status)
	require.NoError(t, configure())
}

func TestCutover_TheSwitchNeedsAFrozenImportAndItsConfigureAuthority(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)

	early := e.doStage(t, console.StageSwitch, "switch-1")
	assert.Equal(t, http.StatusConflict, early.status, early.raw)
	assert.Equal(t, "cutover_stage", early.kind())
	assert.Equal(t, 0, e.cutover.callsTo(console.StageSwitch))
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)

	body := switchBody("switch-1")
	authority := e.authority(console.OperationCutover, e.cutover.Target(legacy, console.StageSwitch), "switch-1", body, "switch-ref")
	withoutConfigure := e.post(t, "switch", body, authority, "")
	assert.Equal(t, http.StatusUnauthorized, withoutConfigure.status, withoutConfigure.raw)
	otherRequest := e.post(t, "switch", body, authority,
		e.authority(console.OperationConfigure, "", "another", body, configureRef))
	assert.Equal(t, http.StatusForbidden, otherRequest.status, otherRequest.raw)
	assert.Equal(t, 0, e.cutover.callsTo(console.StageSwitch), "garam was asked without the configure authority")
	_, ok := e.released(t)
	assert.False(t, ok)

	// Control: a frozen import, with the configure authority for this request, is switched.
	assert.Equal(t, http.StatusOK, e.doStage(t, console.StageSwitch, "switch-1").status)
}

func TestCutover_RollbackOnlyFromFrozen(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)

	rolledBack := e.doStage(t, console.StageRollback, "rollback-1")
	require.Equal(t, http.StatusOK, rolledBack.status, rolledBack.raw)
	assert.Equal(t, "rolled_back", rolledBack.body["stage"])
	_, err := e.definitions.CutoverImportOf(context.Background(), legacy)
	require.ErrorIs(t, err, definition.ErrNotFound)
	_, err = e.definitions.GetDefinition(context.Background(), legacy)
	require.ErrorIs(t, err, definition.ErrNotFound, "the discarded import's revision 1 was kept")

	// After a switch, a rollback is refused here, before garam is asked.
	switched := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, switched.doImport(t, importID, nil).status)
	require.Equal(t, http.StatusOK, switched.doStage(t, console.StageFreeze, "freeze-1").status)
	require.Equal(t, http.StatusOK, switched.doStage(t, console.StageSwitch, "switch-1").status)
	refused := switched.doStage(t, console.StageRollback, "rollback-1")
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, "reverse_migration_required", refused.kind())
	assert.Equal(t, 0, switched.cutover.callsTo(console.StageRollback))
	_, ok := switched.released(t)
	assert.True(t, ok, "a refused rollback withdrew the release")
}

func TestCutover_GaramsRefusalsPassThroughAndRecordNothing(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		kind   string
	}{
		{"another attempt open", &console.CutoverRefusal{Status: http.StatusConflict, Kind: "attempt_open"}, http.StatusConflict, "attempt_open"},
		{"an ended attempt", &console.CutoverRefusal{Status: http.StatusConflict, Kind: "attempt_ended"}, http.StatusConflict, "attempt_ended"},
		{"undecided", console.ErrCutoverUndecided, http.StatusServiceUnavailable, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCutoverEnv(t, map[string]string{})
			require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
			e.cutover.refuse[console.StageFreeze] = tt.err
			refused := e.doStage(t, console.StageFreeze, "freeze-1")
			assert.Equal(t, tt.status, refused.status, refused.raw)
			assert.Equal(t, tt.kind, refused.kind())
			imp, err := e.definitions.CutoverImportOf(context.Background(), legacy)
			require.NoError(t, err)
			assert.Equal(t, definition.CutoverImported, imp.Stage, "a refused freeze was recorded")

			// Control: once garam freezes, the freeze is recorded.
			delete(e.cutover.refuse, console.StageFreeze)
			assert.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-2").status)
		})
	}
}

func TestCutover_AnAuthorityBindingNoAssignmentRefused(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	body := e.importBody(importID, nil)
	authority := e.authority(console.OperationCutover, e.cutover.Target(legacy, console.StageRead), importID, body, "read-ref")
	unassigned := e.introspector.answers[authority]
	unassigned.binding.Assignment = nil
	e.introspector.set(authority, unassigned.binding, nil)

	refused := e.post(t, "import", body, authority, "")
	assert.Equal(t, http.StatusForbidden, refused.status, refused.raw)
	assert.Contains(t, refused.raw, "assignment")
	assert.Equal(t, 0, e.cutover.callsTo(console.StageRead))

	// Control: the same authority binding the agent's assignment imports.
	assert.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
}

func TestCutover_AFreezeAfterTheSwitchRefusedHere(t *testing.T) {
	e := newCutoverEnv(t, map[string]string{})
	require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)

	// Control: an imported agent is frozen, and a repeated freeze is answered.
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-2").status)
	require.Equal(t, http.StatusOK, e.doStage(t, console.StageSwitch, "switch-1").status)
	freezes := e.cutover.callsTo(console.StageFreeze)

	refused := e.doStage(t, console.StageFreeze, "freeze-3")
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, "cutover_stage", refused.kind())
	assert.Equal(t, freezes, e.cutover.callsTo(console.StageFreeze), "garam was asked to freeze a switched agent")
}

// attemptRolledBack is the stage garam records a rolled-back attempt at.
const attemptRolledBack = "rolled_back"

// TestCutover_ARollbackGaramAlreadyHoldsCompletesTheDiscard is #286: garam rolled the attempt back,
// and the control service stopped before it discarded the import. The retried rollback completes
// the discard on garam's reason that the rollback holds, and on nothing else.
func TestCutover_ARollbackGaramAlreadyHoldsCompletesTheDiscard(t *testing.T) {
	frozen := func(t *testing.T) *cutoverEnv {
		t.Helper()
		e := newCutoverEnv(t, map[string]string{})
		require.Equal(t, http.StatusCreated, e.doImport(t, importID, nil).status)
		require.Equal(t, http.StatusOK, e.doStage(t, console.StageFreeze, "freeze-1").status)
		return e
	}
	kept := func(t *testing.T, e *cutoverEnv) {
		t.Helper()
		imp, err := e.definitions.CutoverImportOf(context.Background(), legacy)
		require.NoError(t, err, "a refused rollback discarded the import")
		assert.Equal(t, definition.CutoverFrozen, imp.Stage)
		_, err = e.definitions.GetDefinition(context.Background(), legacy)
		require.NoError(t, err, "a refused rollback discarded revision 1")
	}

	e := frozen(t)
	e.cutover.mu.Lock()
	e.cutover.attempts[importID] = attemptRolledBack
	e.cutover.mu.Unlock()
	resumed := e.doStage(t, console.StageRollback, "rollback-1")
	require.Equal(t, http.StatusOK, resumed.status, resumed.raw)
	assert.Equal(t, "rolled_back", resumed.body["stage"])
	assert.Equal(t, 1, e.cutover.callsTo(console.StageRollback))
	_, err := e.definitions.CutoverImportOf(context.Background(), legacy)
	require.ErrorIs(t, err, definition.ErrNotFound, "the import outlived garam's rollback")
	_, err = e.definitions.GetDefinition(context.Background(), legacy)
	require.ErrorIs(t, err, definition.ErrNotFound, "the import's revision 1 outlived garam's rollback")

	// Control: garam switched the attempt and the control service stopped before it stored the
	// switch. garam's refusal is reverse_migration_required, and the import is kept.
	switched := frozen(t)
	switched.cutover.mu.Lock()
	switched.cutover.attempts[importID] = attemptSwitched
	switched.cutover.mu.Unlock()
	refused := switched.doStage(t, console.StageRollback, "rollback-1")
	assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
	assert.Equal(t, "reverse_migration_required", refused.kind())
	kept(t, switched)

	// Control: only the reason decides. A message or an errorx kind saying the attempt ended, under
	// another reason or none, is refused as garam gave it.
	for name, err := range map[string]error{
		"a message saying so under another reason": &console.CutoverRefusal{Status: http.StatusConflict,
			Kind: "attempt_open", Reason: "attempt_open", Message: "the cutover attempt has been rolled back"},
		"an errorx kind saying so under no reason": &console.CutoverRefusal{Status: http.StatusConflict,
			Kind: "attempt_ended", Message: "the cutover attempt has been rolled back"},
	} {
		t.Run(name, func(t *testing.T) {
			other := frozen(t)
			other.cutover.refuse[console.StageRollback] = err
			refused := other.doStage(t, console.StageRollback, "rollback-1")
			assert.Equal(t, http.StatusConflict, refused.status, refused.raw)
			kept(t, other)
		})
	}
}
