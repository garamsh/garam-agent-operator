package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// configureRequest is the body of a configure request.
type configureRequest struct {
	RequestID        string        `json:"requestId"`
	ExpectedRevision string        `json:"expectedRevision"`
	Profile          profileRef    `json:"profile"`
	Configuration    configuration `json:"configuration"`
}

type profileRef struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
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

// configureResponse is the answer to an applied configure request, and to every repeat of it.
type configureResponse struct {
	Agent    string `json:"agent"`
	Revision string `json:"revision"`
}

// errInvalidBody is returned for a body that is not one configure request.
var errInvalidBody = errors.New("request body is not a configure request")

func (s *server) configure(w http.ResponseWriter, r *http.Request) {
	org, agent := r.PathValue("org"), r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return
	}
	b, err := s.authorize(r.Context(), r, body, target{org: org, operation: OperationConfigure, grn: agent})
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, expected, err := parseConfigure(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if in.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: "request id"})
		return
	}
	applied, err := s.definitions.Configure(r.Context(), definition.ConfigureInput{
		Request: definition.RequestKey{Organization: org, RequestID: b.RequestID},
		Binding: definition.Binding{
			Actor:        b.Actor,
			Operation:    b.Operation,
			Target:       b.Target,
			BodySHA256:   b.BodySHA256,
			OperationRef: b.OperationRef,
			Assignment:   definition.Assignment{Operator: b.Assignment.Operator, Epoch: b.Assignment.Epoch},
		},
		Agent:            definition.GRN(agent),
		ExpectedRevision: expected,
		Profile:          definition.ProfileRef{Name: in.Profile.Name, Version: definition.Version(in.Profile.Version)},
		Config: definition.Configuration{
			Model: definition.Model{
				Provider: in.Configuration.Model.Provider,
				BaseURL:  in.Configuration.Model.BaseURL,
				Name:     in.Configuration.Model.Name,
				APIKey:   definition.SecretRef(in.Configuration.Model.APIKeyRef),
			},
			Ego:   in.Configuration.Ego,
			Tools: in.Configuration.Tools,
		},
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, configureResponse{Agent: agent, Revision: applied.Revision.String()})
}

// parseConfigure reads exactly one configure request, refusing a field it does not know and an
// expected revision that is not a canonical decimal string.
func parseConfigure(body []byte) (configureRequest, definition.Revision, error) {
	var in configureRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() || in.RequestID == "" {
		return configureRequest{}, 0, errInvalidBody
	}
	expected, err := definition.ParseRevision(in.ExpectedRevision)
	if err != nil {
		return configureRequest{}, 0, errInvalidBody
	}
	return in, expected, nil
}
