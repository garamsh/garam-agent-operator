package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// publishRequest is the body of a publish request, as the console's publish form sends it
// (garam@33b1c41 web/src/app/console/orgs/[org]/agents/_components/publish-template-form.tsx).
type publishRequest struct {
	RequestID     string        `json:"requestId"`
	Profile       profileRef    `json:"profile"`
	Configuration configuration `json:"configuration"`
}

// errInvalidPublish is returned for a body that is not one publish request.
var errInvalidPublish = errors.New("request body is not a publish request")

func (s *server) publish(w http.ResponseWriter, r *http.Request) {
	b, body, ok := s.authorized(w, r, OperationTemplatePublish, orgGRN(r))
	if !ok {
		return
	}
	var in publishRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() || in.RequestID == "" {
		s.respondError(w, errInvalidPublish)
		return
	}
	if in.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: fieldRequestID})
		return
	}
	org, name := r.PathValue("org"), r.PathValue("name")
	p, created, err := s.definitions.Publish(r.Context(), definition.PublishInput{
		Request: definition.RequestKey{Organization: org, RequestID: b.RequestID},
		Binding: definition.Binding{
			Actor:        b.Actor,
			Operation:    b.Operation,
			Target:       b.Target,
			BodySHA256:   b.BodySHA256,
			OperationRef: b.OperationRef,
		},
		Template: definition.Template{
			Name:    name,
			Profile: definition.ProfileRef{Name: in.Profile.Name, Version: definition.Version(in.Profile.Version)},
			Config: definition.Configuration{
				Model: definition.Model{
					Provider:  in.Configuration.Model.Provider,
					BaseURL:   in.Configuration.Model.BaseURL,
					Name:      in.Configuration.Model.Name,
					APIKey:    definition.SecretRef(in.Configuration.Model.APIKeyRef),
					Embedding: embeddingOf(in.Configuration.Model.Embedding),
				},
				Ego:   in.Configuration.Ego,
				Tools: in.Configuration.Tools,
			},
		},
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, versionRef{Name: p.Template.Name, Version: int64(p.Template.Version)})
}
