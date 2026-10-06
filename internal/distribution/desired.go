package distribution

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// maxWait bounds how long one request for the desired feed waits for the position to move.
const maxWait = 30 * time.Second

// desiredResponse is the answer to a request for the desired feed: the controller's whole
// releasable set as of cursor.
type desiredResponse struct {
	Cursor string         `json:"cursor"`
	Agents []desiredAgent `json:"agents"`
}

type desiredAgent struct {
	Agent         string        `json:"agent"`
	Revision      string        `json:"revision"`
	Epoch         string        `json:"epoch"`
	Profile       profile       `json:"profile"`
	Configuration configuration `json:"configuration"`
	// Origin is "cutover" for an agent garam recorded as switched from its legacy source, and
	// absent otherwise: the manager takes such an agent over from the source it was built from
	// (ADR 0050).
	Origin string `json:"origin,omitempty"`
}

// originCutover is the origin of an agent whose revisions began with a cutover import.
const originCutover = "cutover"

type profile struct {
	Name             string                      `json:"name"`
	Version          int64                       `json:"version"`
	Resources        corev1.ResourceRequirements `json:"resources"`
	StorageSize      string                      `json:"storageSize"`
	StorageClassName *string                     `json:"storageClassName"`
	// WorkspaceStorageSize is absent where the profile leaves the workspace claim to StorageSize
	// (ADR 0053).
	WorkspaceStorageSize *string `json:"workspaceStorageSize,omitempty"`
}

type configuration struct {
	Model model             `json:"model"`
	Ego   string            `json:"ego"`
	Tools map[string]string `json:"tools"`
}

type model struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"baseUrl"`
	Name      string `json:"name"`
	APIKeyRef string `json:"apiKeyRef"`
}

var (
	// errInvalidQuery is returned for a cursor or wait the feed cannot answer.
	errInvalidQuery = errors.New("cursor or waitSeconds is not one the feed answers")

	// errTooManyAgents is returned when a controller has more candidate agents than one answer
	// carries; a partial set would read to the controller as the rest withdrawn.
	errTooManyAgents = errors.New("controller has more agents than one desired answer carries")
)

// desired answers the controller's whole releasable set: the latest revision of every agent recorded
// for it that garam proves placed on it, under that revision's epoch, for this answer. A withheld
// agent is absent from this answer and decided again on the next. The cursor only says when to ask:
// a request after it waits until the position moves past it, or waitSeconds passes.
func (s *server) desired(w http.ResponseWriter, r *http.Request) {
	after, wait, err := parseFeedQuery(r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.waitPast(r.Context(), after, wait); err != nil {
		s.respondError(w, err)
		return
	}
	page, err := s.definitions.Desired(r.Context(), c.grn, s.maxAgents+1)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if len(page.Revisions) > s.maxAgents {
		s.respondError(w, errTooManyAgents)
		return
	}
	out := desiredResponse{Cursor: strconv.FormatInt(int64(page.Position), 10), Agents: []desiredAgent{}}
	for _, d := range page.Revisions {
		released, err := s.placed(r.Context(), c, string(d.Definition.Agent), d.Definition.Assignment.Epoch)
		if err != nil {
			s.respondError(w, err)
			return
		}
		if released {
			out.Agents = append(out.Agents, desiredAgentOf(d))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// waitPast returns once the position has moved past after, reading it every poll interval, or once
// wait has passed. A request with no cursor does not wait.
func (s *server) waitPast(ctx context.Context, after *definition.Position, wait time.Duration) error {
	if after == nil {
		return nil
	}
	deadline := time.Now().Add(wait)
	for {
		position, err := s.definitions.Position(ctx)
		if err != nil {
			return err
		}
		if position < *after {
			return errInvalidQuery
		}
		if position > *after || !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(min(s.pollInterval, time.Until(deadline))):
		}
	}
}

func parseFeedQuery(r *http.Request) (*definition.Position, time.Duration, error) {
	q := r.URL.Query()
	var after *definition.Position
	if v := q.Get("after"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != v {
			return nil, 0, errInvalidQuery
		}
		p := definition.Position(parsed)
		after = &p
	}
	var wait time.Duration
	if v := q.Get("waitSeconds"); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil || seconds < 0 || time.Duration(seconds)*time.Second > maxWait {
			return nil, 0, errInvalidQuery
		}
		wait = time.Duration(seconds) * time.Second
	}
	return after, wait, nil
}

func desiredAgentOf(d definition.DesiredRevision) desiredAgent {
	def, settings := d.Definition, d.Settings
	origin := ""
	if d.Cutover {
		origin = originCutover
	}
	var workspaceSize *string
	if settings.WorkspaceStorageSize != nil {
		size := settings.WorkspaceStorageSize.String()
		workspaceSize = &size
	}
	return desiredAgent{
		Origin:   origin,
		Agent:    string(def.Agent),
		Revision: def.Revision.String(),
		Epoch:    def.Assignment.Epoch,
		Profile: profile{
			Name:             def.Profile.Name,
			Version:          int64(def.Profile.Version),
			Resources:        settings.Resources,
			StorageSize:      settings.StorageSize.String(),
			StorageClassName: settings.StorageClassName,

			WorkspaceStorageSize: workspaceSize,
		},
		Configuration: configuration{
			Model: model{
				Provider:  def.Config.Model.Provider,
				BaseURL:   def.Config.Model.BaseURL,
				Name:      def.Config.Model.Name,
				APIKeyRef: string(def.Config.Model.APIKey),
			},
			Ego:   def.Config.Ego,
			Tools: def.Config.Tools,
		},
	}
}
