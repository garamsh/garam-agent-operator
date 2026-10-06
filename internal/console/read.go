package console

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// The answers the read routes give, in the field names the console parses
// (garam@33b1c41 web/src/lib/operation-authority.ts).
type (
	templateSummary struct {
		Name    string     `json:"name"`
		Version int64      `json:"version"`
		Profile versionRef `json:"profile"`
	}
	templatesAnswer struct {
		Templates []templateSummary `json:"templates"`
	}
	templateAnswer struct {
		Name          string        `json:"name"`
		Version       int64         `json:"version"`
		Profile       versionRef    `json:"profile"`
		Configuration configuration `json:"configuration"`
	}
	profilesAnswer struct {
		Profiles []versionRef `json:"profiles"`
	}
	profileAnswer struct {
		Name             string                      `json:"name"`
		Version          int64                       `json:"version"`
		Resources        corev1.ResourceRequirements `json:"resources"`
		StorageSize      string                      `json:"storageSize"`
		StorageClassName *string                     `json:"storageClassName"`
		// WorkspaceStorageSize is absent where the profile leaves the workspace claim to StorageSize.
		WorkspaceStorageSize *string `json:"workspaceStorageSize,omitempty"`
	}
	executionAnswer struct {
		Desired   desiredExecution    `json:"desired"`
		Rendered  *renderedExecution  `json:"rendered"`
		Effective *effectiveExecution `json:"effective"`
	}
	desiredExecution struct {
		Revision string `json:"revision"`
		Pending  bool   `json:"pending"`
	}
	renderedExecution struct {
		Revision string `json:"revision"`
	}
	effectiveExecution struct {
		Revision   string    `json:"revision"`
		Generation string    `json:"generation"`
		ObservedAt time.Time `json:"observedAt"`
	}
)

// authorized reads the body, which for a read is empty, and authorizes the request for operation
// on grn in the path's organization. It answers the refusal itself and reports false where it did.
func (s *server) authorized(w http.ResponseWriter, r *http.Request, operation, grn string) (Binding, []byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return Binding{}, nil, false
	}
	// The raw origin-form target, as it arrived and before the router decoded anything, is the
	// one the authority must bind (garam@33b1c41 api/machine.yaml OperationBinding.requestTarget).
	b, err := s.authorize(r.Context(), r, body, target{
		org: r.PathValue("org"), operation: operation, grn: grn, requestTarget: r.RequestURI,
	})
	if err != nil {
		s.respondError(w, err)
		return Binding{}, nil, false
	}
	return b, body, true
}

// orgGRN is the GRN of the organization the path names, which the organization's reads and
// publication target.
func orgGRN(r *http.Request) string {
	return orgGRNPrefix + r.PathValue("org")
}

func (s *server) listTemplates(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorized(w, r, OperationTemplateRead, orgGRN(r)); !ok {
		return
	}
	templates, err := s.definitions.ListTemplates(r.Context(), r.PathValue("org"))
	if err != nil {
		s.respondError(w, err)
		return
	}
	out := templatesAnswer{Templates: []templateSummary{}}
	for _, t := range templates {
		out.Templates = append(out.Templates, templateSummary{
			Name: t.Name, Version: int64(t.Version),
			Profile: versionRef{Name: t.Profile.Name, Version: int64(t.Profile.Version)},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getTemplate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorized(w, r, OperationTemplateRead, orgGRN(r)); !ok {
		return
	}
	version, err := parseVersion(r.PathValue("version"))
	if err != nil {
		s.respondError(w, err)
		return
	}
	t, err := s.definitions.GetTemplate(r.Context(), r.PathValue("org"),
		definition.TemplateRef{Name: r.PathValue("name"), Version: version})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, templateAnswer{
		Name: t.Name, Version: int64(t.Version),
		Profile:       versionRef{Name: t.Profile.Name, Version: int64(t.Profile.Version)},
		Configuration: configurationOf(t.Config),
	})
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorized(w, r, OperationProfileRead, orgGRN(r)); !ok {
		return
	}
	refs, err := s.definitions.ListProfiles(r.Context(), r.PathValue("org"))
	if err != nil {
		s.respondError(w, err)
		return
	}
	out := profilesAnswer{Profiles: []versionRef{}}
	for _, ref := range refs {
		out.Profiles = append(out.Profiles, versionRef{Name: ref.Name, Version: int64(ref.Version)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getProfile(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorized(w, r, OperationProfileRead, orgGRN(r)); !ok {
		return
	}
	version, err := parseVersion(r.PathValue("version"))
	if err != nil {
		s.respondError(w, err)
		return
	}
	p, err := s.definitions.GetProfile(r.Context(), r.PathValue("org"),
		definition.ProfileRef{Name: r.PathValue("name"), Version: version})
	if err != nil {
		s.respondError(w, err)
		return
	}
	answer := profileAnswer{
		Name: p.Name, Version: int64(p.Version),
		Resources: p.Settings.Resources, StorageSize: p.Settings.StorageSize.String(),
		StorageClassName: p.Settings.StorageClassName,
	}
	if size := p.Settings.WorkspaceStorageSize; size != nil {
		workspace := size.String()
		answer.WorkspaceStorageSize = &workspace
	}
	writeJSON(w, http.StatusOK, answer)
}

func (s *server) execution(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if _, _, ok := s.authorized(w, r, OperationExecutionRead, agent); !ok {
		return
	}
	e, err := s.definitions.Execution(r.Context(), r.PathValue("org"), definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return
	}
	out := executionAnswer{Desired: desiredExecution{Revision: e.Desired.String(), Pending: e.Rendered < e.Desired}}
	if e.Rendered > 0 {
		out.Rendered = &renderedExecution{Revision: e.Rendered.String()}
	}
	if e.Effective != nil {
		out.Effective = &effectiveExecution{
			Revision: e.Effective.Revision.String(), Generation: e.Effective.Generation, ObservedAt: e.Effective.ObservedAt,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// configurationOf is a configuration as the wire carries it. The model's key is the reference to
// where it is held, never a value: the store holds no value to give.
func configurationOf(c definition.Configuration) configuration {
	tools := c.Tools
	if tools == nil {
		tools = definition.ToolPins{}
	}
	return configuration{
		Model: model{
			Provider: c.Model.Provider, BaseURL: c.Model.BaseURL, Name: c.Model.Name, APIKeyRef: string(c.Model.APIKey),
			Embedding: embeddingWireOf(c.Model.Embedding),
		},
		Ego:   c.Ego,
		Tools: tools,
	}
}

// parseVersion reads a version from the path: a canonical decimal of 1 or more. Anything else
// names no version, so it is ErrNotFound.
func parseVersion(s string) (definition.Version, error) {
	if s == "" || s[0] == '0' || strings.TrimLeft(s, "0123456789") != "" {
		return 0, definition.ErrNotFound
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, definition.ErrNotFound
	}
	return definition.Version(n), nil
}

// embeddingWireOf is an embeddings endpoint as the wire carries it, nil where there is none. Its
// key is the reference to where it is held, as the model's is.
func embeddingWireOf(e *definition.Embedding) *embedding {
	if e == nil {
		return nil
	}
	return &embedding{BaseURL: e.BaseURL, Name: e.Name, APIKeyRef: string(e.APIKey)}
}
