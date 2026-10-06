package console

import (
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// The kinds a stop or a start is refused under.
const (
	kindAgentStopped    = "agent_stopped"
	kindAgentNotStopped = "agent_not_stopped"
)

// stopRequest is the body of a stop and of a start.
type stopRequest struct {
	RequestID string `json:"requestId"`
}

// stopAnswer is an agent's stop as stored: whether it still holds the agent, the activation it
// recorded as the agent's latest, and whether garam answered that activation's deactivation.
type stopAnswer struct {
	Agent        string  `json:"agent"`
	Stopped      bool    `json:"stopped"`
	ActivationID *string `json:"activationId"`
	Deactivated  bool    `json:"deactivated"`
}

func stopAnswerOf(s definition.Stop) stopAnswer {
	a := stopAnswer{Agent: string(s.Agent), Stopped: s.Start == nil, Deactivated: s.Deactivated || s.ActivationID == ""}
	if s.ActivationID != "" {
		a.ActivationID = &s.ActivationID
	}
	return a
}

// stop stops an agent without a replacement under agent:configure: the stop is recorded first,
// so from its commit nothing of the agent is activated and the feed tells its controller to keep
// the runtime stopped; then garam is asked to deactivate the activation the stop recorded, so it
// stops routing to the agent. A repeat of the request finishes a deactivation garam left
// undecided, and is decided on garam's answer alone.
func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	in, ok := s.stopInput(w, r)
	if !ok {
		return
	}
	stop, _, err := s.definitions.Stop(r.Context(), in)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if stop.Start == nil && !stop.Deactivated && stop.ActivationID != "" {
		if err := s.lifecycle.Deactivate(r.Context(), string(in.Agent), stop.ActivationID); err != nil {
			s.respondError(w, err)
			return
		}
		if err := s.definitions.RecordDeactivation(r.Context(), in.Key); err != nil {
			s.respondError(w, err)
			return
		}
		stop.Deactivated = true
	}
	writeJSON(w, http.StatusOK, stopAnswerOf(stop))
}

// start ends the stop holding an agent under agent:configure. The stop is kept; the agent's next
// activation is admitted, and the feed no longer tells its controller to keep it stopped.
func (s *server) start(w http.ResponseWriter, r *http.Request) {
	in, ok := s.stopInput(w, r)
	if !ok {
		return
	}
	stop, err := s.definitions.Start(r.Context(), in)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stopAnswerOf(stop))
}

// stopInput authorizes a stop or a start under agent:configure, and reads its request.
func (s *server) stopInput(w http.ResponseWriter, r *http.Request) (definition.StopInput, bool) {
	org, agent := r.PathValue("org"), r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		s.respondError(w, errInvalidBody)
		return definition.StopInput{}, false
	}
	b, err := s.authorize(r.Context(), r, body, target{org: org, operation: OperationConfigure, grn: agent})
	if err != nil {
		s.respondError(w, err)
		return definition.StopInput{}, false
	}
	var in stopRequest
	if err := decodeStrict(body, &in); err != nil || in.RequestID == "" {
		s.respondError(w, errInvalidBody)
		return definition.StopInput{}, false
	}
	if in.RequestID != b.RequestID {
		s.respondError(w, &MismatchError{Field: fieldRequestID})
		return definition.StopInput{}, false
	}
	return definition.StopInput{
		Key:     definition.RequestKey{Organization: org, RequestID: b.RequestID},
		Binding: bindingOf(b),
		Agent:   definition.GRN(agent),
	}, true
}
