package console

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// createRequest is the body of a create request.
type createRequest struct {
	RequestID  string     `json:"requestId"`
	Controller string     `json:"controller"`
	Template   versionRef `json:"template"`
	Profile    versionRef `json:"profile"`
}

type versionRef struct {
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

// createResponse is the answer to a create request, and to every identical repeat of it.
type createResponse struct {
	Agent    string `json:"agent"`
	Revision string `json:"revision"`
	Epoch    string `json:"epoch"`
}

// create creates an agent assigned to the request's controller, from a template version under a
// profile version. The authority binds agent:create on that controller, which is read from the
// body, so it is compared once the body's digest has proved the body is the one bound.
func (s *server) create(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return
	}
	b, err := s.authorize(r.Context(), r, body, target{org: org, operation: OperationCreate})
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseCreate(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	switch {
	case in.RequestID != b.RequestID:
		s.respondError(w, &MismatchError{Field: "request id"})
		return
	case in.Controller != b.Target:
		s.respondError(w, &MismatchError{Field: "target"})
		return
	}
	c, first, err := s.definitions.CreateAgent(r.Context(), definition.CreateInput{
		Request: definition.RequestKey{Organization: org, RequestID: b.RequestID},
		Binding: definition.Binding{
			Actor: b.Actor, Operation: b.Operation, Target: b.Target,
			BodySHA256: b.BodySHA256, OperationRef: b.OperationRef,
		},
		Controller: in.Controller,
		Template:   definition.TemplateRef{Name: in.Template.Name, Version: definition.Version(in.Template.Version)},
		Profile:    definition.ProfileRef{Name: in.Profile.Name, Version: definition.Version(in.Profile.Version)},
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	switch outcome := c.Outcome.(type) {
	case definition.Registered:
		status := http.StatusOK
		if first {
			status = http.StatusCreated
		}
		writeJSON(w, status, createResponse{
			Agent: string(outcome.Agent), Revision: definition.Revision(1).String(), Epoch: outcome.Epoch,
		})
	case definition.Failed:
		refusal := definition.ErrRegistrationRefused
		if outcome.Conflict {
			refusal = definition.ErrRegistrationConflict
		}
		s.respondError(w, fmt.Errorf("%w: %s", refusal, outcome.Reason))
	default:
		s.respondError(w, fmt.Errorf("creation %s answered as %T", b.RequestID, outcome))
	}
}

// parseCreate reads exactly one create request, refusing a field it does not know.
func parseCreate(body []byte) (createRequest, error) {
	var in createRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() ||
		in.RequestID == "" || in.Controller == "" || in.Template.Name == "" || in.Profile.Name == "" {
		return createRequest{}, errInvalidBody
	}
	return in, nil
}
