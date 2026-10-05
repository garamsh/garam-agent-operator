package console

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/gowebpki/jcs"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// The kinds a cutover refusal of this service's own is answered under, beside garam's reasons.
const (
	kindImportOpen               = "import_open"
	kindAlreadyDefined           = "already_defined"
	kindCutoverStage             = "cutover_stage"
	kindReverseMigrationRequired = "reverse_migration_required"
	kindDigestMismatch           = "digest_mismatch"
	kindNotCutOverEligible       = "not_cut_over_eligible"
	kindKeyDispositionRequired   = "key_disposition_required"
)

// configureAuthorityHeader carries the switch's agent:configure authority for revision 1, beside
// the agent:cutover authority in Authorization, as `Garam-Operation <authority>` (ADR 0050).
const configureAuthorityHeader = "Garam-Configure-Operation"

var (
	// neverAppliedKey is a key garam's values may hold (`[a-z0-9-]`) and the legacy path never
	// applied: it read only `tools.pins.*` (internal/garam/client.go readValues, at 85cf8c9).
	neverAppliedKey = regexp.MustCompile(`^[a-z0-9-]+$`)

	// pinPrefix is the key family the legacy path applied at an agent's first construction.
	pinPrefix = "tools.pins."
)

// stageRequest is the body of the freeze, the switch and the rollback.
type stageRequest struct {
	RequestID string `json:"requestId"`
}

// importRequest is the body of the import: the profile revision 1 runs under, and the
// disposition of each value garam's legacy path may have applied.
type importRequest struct {
	RequestID    string                            `json:"requestId"`
	Profile      versionRef                        `json:"profile"`
	Dispositions map[string]definition.Disposition `json:"dispositions"`
}

// cutoverResponse is a stage's answer: the import, at the stage it is now.
type cutoverResponse struct {
	Agent        string                            `json:"agent"`
	ImportID     string                            `json:"importId"`
	Stage        string                            `json:"stage"`
	SourceDigest string                            `json:"sourceDigest"`
	Revision     string                            `json:"revision"`
	Dispositions map[string]definition.Disposition `json:"dispositions"`
}

// errNoDispositions is a refusal naming the keys an import needs a disposition for.
func errNoDispositions(keys []string) error {
	return &CutoverRefusal{Status: http.StatusUnprocessableEntity, Kind: kindKeyDispositionRequired,
		Message: "these values need a disposition, import or archive: " + strings.Join(keys, ", ")}
}

// cutoverRequest authorizes a stage's request: an agent:cutover authority on the agent, bound to
// garam's route for stage, for exactly this body. It answers the agent, the binding and the body.
func (s *server) cutoverRequest(w http.ResponseWriter, r *http.Request, stage Stage) (string, Binding, []byte, bool) {
	org, agent := r.PathValue("org"), r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return "", Binding{}, nil, false
	}
	b, err := s.authorize(r.Context(), r, body, target{
		org: org, operation: OperationCutover, grn: agent, requestTarget: s.cutover.Target(agent, stage),
	})
	if err != nil {
		s.respondError(w, err)
		return "", Binding{}, nil, false
	}
	return agent, b, body, true
}

// importCutover reads a legacy agent's source from garam, verifies its digest, and stores it as
// the agent's cutover import with an inactive revision 1.
func (s *server) importCutover(w http.ResponseWriter, r *http.Request) {
	agent, b, body, ok := s.cutoverRequest(w, r, StageRead)
	if !ok {
		return
	}
	var in importRequest
	if err := decodeStrict(body, &in); err != nil || in.RequestID != b.RequestID || in.Profile.Name == "" {
		s.respondError(w, errInvalidBody)
		return
	}
	source, err := s.cutover.Read(r.Context(), agent, b.OperationRef)
	if err != nil {
		s.respondError(w, err)
		return
	}
	imp, err := importOf(agent, source, in)
	if err != nil {
		s.respondError(w, err)
		return
	}
	imp.Organization = r.PathValue("org")
	stored, first, err := s.definitions.ImportCutover(r.Context(), imp)
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.answerCutover(w, stored, first)
}

// importOf is the import of source the request asks for, once source is eligible, its digest is
// the one garam answered, and every value it holds has a disposition.
func importOf(agent string, source CutoverSource, in importRequest) (definition.CutoverImport, error) {
	if source.Agent != agent || source.Mode != "legacy" || source.Class != "eligible" || source.SourceDigest == "" {
		return definition.CutoverImport{}, &CutoverRefusal{Status: http.StatusUnprocessableEntity, Kind: kindNotCutOverEligible,
			Message: fmt.Sprintf("garam answers the agent %s, %s", source.Mode, source.Class)}
	}
	operator := source.Assignee
	if source.HasSource {
		operator = source.Operator
	}
	digest, err := sourceDigest(agent, operator, source.Values)
	if err != nil {
		return definition.CutoverImport{}, err
	}
	if digest != source.SourceDigest {
		return definition.CutoverImport{}, &CutoverRefusal{Status: http.StatusConflict, Kind: kindDigestMismatch,
			Message: "the source's digest is not the one garam answered"}
	}
	dispositions, pins, err := dispose(source.Values, in.Dispositions)
	if err != nil {
		return definition.CutoverImport{}, err
	}
	return definition.CutoverImport{
		Agent: definition.GRN(agent), ImportID: in.RequestID, Epoch: source.Epoch, Assignee: source.Assignee,
		SourceDigest: digest, Values: source.Values, Dispositions: dispositions, Pins: pins,
		Profile: definition.ProfileRef{Name: in.Profile.Name, Version: definition.Version(in.Profile.Version)},
	}, nil
}

// sourceDigest is the lowercase hex SHA-256 of the RFC 8785 JSON of {agentGrn, operatorGrn,
// values}, as garam computes it (garam@1a5273d, getAgentCutover).
func sourceDigest(agent, operator string, values map[string]string) (string, error) {
	if values == nil {
		values = map[string]string{}
	}
	raw, err := json.Marshal(struct {
		AgentGRN    string            `json:"agentGrn"`
		OperatorGRN string            `json:"operatorGrn"`
		Values      map[string]string `json:"values"`
	}{agent, operator, values})
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalize the source: %v", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// dispose is what the import does with each value: a `[a-z0-9-]` key the legacy path never applied
// is archived as such, and any other key needs the request's disposition, import or archive.
// Only a `tools.pins.<tool>` key may be imported, into revision 1's pins.
func dispose(values map[string]string, asked map[string]definition.Disposition) (
	map[string]definition.Disposition, definition.ToolPins, error) {
	dispositions := map[string]definition.Disposition{}
	var (
		pins    definition.ToolPins
		missing []string
	)
	for key := range asked {
		if _, held := values[key]; !held {
			return nil, nil, errInvalidBody
		}
	}
	for key, value := range values {
		if neverAppliedKey.MatchString(key) {
			if d, ok := asked[key]; ok && d != definition.DispositionArchive {
				return nil, nil, errInvalidBody
			}
			dispositions[key] = definition.DispositionNeverApplied
			continue
		}
		switch asked[key] {
		case definition.DispositionArchive:
			dispositions[key] = definition.DispositionArchive
		case definition.DispositionImport:
			tool, isPin := strings.CutPrefix(key, pinPrefix)
			if !isPin || tool == "" {
				return nil, nil, errInvalidBody
			}
			if pins == nil {
				pins = definition.ToolPins{}
			}
			pins[tool] = value
			dispositions[key] = definition.DispositionImport
		case "":
			missing = append(missing, key)
		default:
			return nil, nil, errInvalidBody
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, nil, errNoDispositions(missing)
	}
	return dispositions, pins, nil
}

// freezeCutover has garam freeze the agent's source against its import's digest.
func (s *server) freezeCutover(w http.ResponseWriter, r *http.Request) {
	agent, b, imp, ok := s.stage(w, r, StageFreeze)
	if !ok {
		return
	}
	if imp.Stage == definition.CutoverSwitched {
		s.respondError(w, definition.ErrCutoverStage)
		return
	}
	if _, err := s.cutover.Freeze(r.Context(), agent, b.OperationRef, imp.ImportID, imp.SourceDigest, imp.Epoch); err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.definitions.FreezeCutover(r.Context(), definition.GRN(agent), imp.ImportID); err != nil {
		s.respondError(w, err)
		return
	}
	imp.Stage = definition.CutoverFrozen
	s.answerCutover(w, imp, false)
}

// switchCutover has garam record the switch, and only on its answer activates revision 1 under the
// agent:configure authority the request also carries, whose reference its first activation is
// sent under.
func (s *server) switchCutover(w http.ResponseWriter, r *http.Request) {
	agent, b, imp, ok := s.stage(w, r, StageSwitch)
	if !ok {
		return
	}
	if imp.Stage != definition.CutoverFrozen && imp.Stage != definition.CutoverSwitched {
		s.respondError(w, definition.ErrCutoverStage)
		return
	}
	configure, err := s.authorizeHeader(r.Context(), r.Header.Get(configureAuthorityHeader), b.body, target{
		org: r.PathValue("org"), operation: OperationConfigure, grn: agent,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	if configure.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: "configure authority's request id"})
		return
	}
	if _, err := s.cutover.Switch(r.Context(), agent, b.OperationRef, imp.ImportID); err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.definitions.SwitchCutover(r.Context(), definition.GRN(agent), imp.ImportID, configure.OperationRef); err != nil {
		s.respondError(w, err)
		return
	}
	imp.Stage = definition.CutoverSwitched
	s.answerCutover(w, imp, false)
}

// rollBackCutover has garam end a frozen attempt, then discards the import and its revision 1. A
// switched import is refused here before garam is asked.
func (s *server) rollBackCutover(w http.ResponseWriter, r *http.Request) {
	agent, b, imp, ok := s.stage(w, r, StageRollback)
	if !ok {
		return
	}
	if imp.Stage == definition.CutoverSwitched {
		s.respondError(w, definition.ErrReverseMigrationRequired)
		return
	}
	if _, err := s.cutover.RollBack(r.Context(), agent, b.OperationRef, imp.ImportID); err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.definitions.RollBackCutover(r.Context(), definition.GRN(agent), imp.ImportID); err != nil {
		s.respondError(w, err)
		return
	}
	imp.Stage = "rolled_back"
	s.answerCutover(w, imp, false)
}

// stagedBinding is an authorized stage request's binding, with the body it was authorized for.
type stagedBinding struct {
	Binding
	body []byte
}

// stage authorizes a freeze, a switch or a rollback, and reads the agent's import.
func (s *server) stage(w http.ResponseWriter, r *http.Request, stage Stage) (string, stagedBinding, definition.CutoverImport, bool) {
	agent, b, body, ok := s.cutoverRequest(w, r, stage)
	if !ok {
		return "", stagedBinding{}, definition.CutoverImport{}, false
	}
	var in stageRequest
	if err := decodeStrict(body, &in); err != nil || in.RequestID != b.RequestID {
		s.respondError(w, errInvalidBody)
		return "", stagedBinding{}, definition.CutoverImport{}, false
	}
	imp, err := s.definitions.CutoverImportOf(r.Context(), definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return "", stagedBinding{}, definition.CutoverImport{}, false
	}
	return agent, stagedBinding{Binding: b, body: body}, imp, true
}

func (s *server) answerCutover(w http.ResponseWriter, imp definition.CutoverImport, first bool) {
	status := http.StatusOK
	if first {
		status = http.StatusCreated
	}
	writeJSON(w, status, cutoverResponse{
		Agent: string(imp.Agent), ImportID: imp.ImportID, Stage: string(imp.Stage), SourceDigest: imp.SourceDigest,
		Revision: definition.Revision(1).String(), Dispositions: imp.Dispositions,
	})
}

// decodeStrict reads exactly one JSON value into out, refusing a field it does not know.
func decodeStrict(body []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("more than one value")
	}
	return nil
}
