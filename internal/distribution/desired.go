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

// maxWait bounds how long one request for the desired feed waits for something new.
const maxWait = 30 * time.Second

// desiredResponse is the answer to a request for the desired feed.
type desiredResponse struct {
	Cursor string         `json:"cursor"`
	Agents []desiredAgent `json:"agents"`
}

type desiredAgent struct {
	Agent         string        `json:"agent"`
	Revision      int64         `json:"revision"`
	Epoch         string        `json:"epoch"`
	Profile       profile       `json:"profile"`
	Configuration configuration `json:"configuration"`
}

type profile struct {
	Name             string                      `json:"name"`
	Version          int64                       `json:"version"`
	Resources        corev1.ResourceRequirements `json:"resources"`
	StorageSize      string                      `json:"storageSize"`
	StorageClassName *string                     `json:"storageClassName"`
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

// errInvalidQuery is returned for a cursor or wait the feed cannot answer.
var errInvalidQuery = errors.New("cursor or waitSeconds is not one the feed answers")

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
	page, err := s.waitForDesired(r.Context(), c.grn, after, wait)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if page.Position < after {
		s.respondError(w, errInvalidQuery)
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

// waitForDesired reads the feed after a position, and reads it again every poll interval while it
// holds nothing new, until wait has passed.
func (s *server) waitForDesired(ctx context.Context, operator string, after definition.Position, wait time.Duration) (definition.DesiredPage, error) {
	deadline := time.Now().Add(wait)
	for {
		page, err := s.definitions.Desired(ctx, operator, after)
		if err != nil || len(page.Revisions) > 0 || page.Position < after || !time.Now().Before(deadline) {
			return page, err
		}
		select {
		case <-ctx.Done():
			return page, nil
		case <-time.After(min(s.pollInterval, time.Until(deadline))):
		}
	}
}

func parseFeedQuery(r *http.Request) (definition.Position, time.Duration, error) {
	q := r.URL.Query()
	var after int64
	if v := q.Get("after"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, errInvalidQuery
		}
		after = parsed
	}
	var wait time.Duration
	if v := q.Get("waitSeconds"); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil || seconds < 0 || time.Duration(seconds)*time.Second > maxWait {
			return 0, 0, errInvalidQuery
		}
		wait = time.Duration(seconds) * time.Second
	}
	return definition.Position(after), wait, nil
}

func desiredAgentOf(d definition.DesiredRevision) desiredAgent {
	def, settings := d.Definition, d.Settings
	return desiredAgent{
		Agent:    string(def.Agent),
		Revision: int64(def.Revision),
		Epoch:    def.Assignment.Epoch,
		Profile: profile{
			Name:             def.Profile.Name,
			Version:          int64(def.Profile.Version),
			Resources:        settings.Resources,
			StorageSize:      settings.StorageSize.String(),
			StorageClassName: settings.StorageClassName,
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
