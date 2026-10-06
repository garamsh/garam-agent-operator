package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// maxPlacementBytes bounds a placement registration's body.
const maxPlacementBytes = 4 << 10

// sha256Pattern is a SHA-256 digest as the manager writes it: 64 lowercase hex digits.
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// placementRequest is the body of a placement registration. Previous is null for an agent's
// first placement.
type placementRequest struct {
	Epoch       string           `json:"epoch"`
	PodUID      string           `json:"podUid"`
	PVCUID      string           `json:"pvcUid"`
	TokenSHA256 string           `json:"tokenSha256"`
	Previous    *previousRequest `json:"previous"`
}

type previousRequest struct {
	PodUID              string `json:"podUid"`
	WriterStoppedSHA256 string `json:"writerStoppedSha256"`
}

// placementResponse is the placement as stored, for its registration and every repeat of it.
type placementResponse struct {
	Agent       string `json:"agent"`
	Epoch       string `json:"epoch"`
	PodUID      string `json:"podUid"`
	PVCUID      string `json:"pvcUid"`
	TokenSHA256 string `json:"tokenSha256"`
}

// errInvalidPlacementBody is returned for a body that is not one placement registration.
var errInvalidPlacementBody = errors.New("request body is not a placement registration")

// registerPlacement stores the placement the controller registers for an agent placed on it,
// with the controller's leaf as this handshake presented it. The controller is proved per
// request, and the agent's placement on it per decision, before anything is decided.
func (s *server) registerPlacement(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPlacementBytes))
	if err != nil {
		s.respondError(w, errInvalidPlacementBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parsePlacement(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.provePlacedAt(r, c, agent, in.Epoch); err != nil {
		s.respondError(w, err)
		return
	}
	stored, first, err := s.definitions.RegisterPlacement(r.Context(), definition.PlacementInput{
		Agent: definition.GRN(agent), Controller: c.grn, Request: in, LeafDER: c.leafDER,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	status := http.StatusOK
	if first {
		status = http.StatusCreated
	}
	writeJSON(w, status, placementResponse{
		Agent: agent, Epoch: stored.Request.Epoch, PodUID: stored.Request.PodUID,
		PVCUID: stored.Request.PVCUID, TokenSHA256: stored.Request.TokenSHA256,
	})
}

// provePlacedAt requires the agent's latest revision to be recorded for c, and garam's
// agent-bound proof, obtained for this decision, to prove it placed there under epoch and under
// the revision's epoch. A refused proof is errNotPlaced; a proof of another epoch is
// errEpochSuperseded.
func (s *server) provePlacedAt(r *http.Request, c controller, agent, epoch string) error {
	latest, err := s.definitions.GetDefinition(r.Context(), definition.GRN(agent))
	if err != nil {
		return err
	}
	if latest.Assignment == nil || latest.Assignment.Operator != c.grn {
		return errNotPlaced
	}
	proved, err := s.provedEpoch(r.Context(), c, agent)
	if errors.Is(err, ErrNotProved) {
		return errNotPlaced
	}
	if err != nil {
		return err
	}
	if proved != epoch || proved != latest.Assignment.Epoch {
		return errEpochSuperseded
	}
	return nil
}

// parsePlacement reads exactly one placement registration, refusing a field it does not know, a
// digest that is not 64 lowercase hex digits, and a placement replacing itself. A replaced
// placement's evidence digest may be absent, which the decision refuses as evidence missing.
func parsePlacement(body []byte) (definition.PlacementRequest, error) {
	var in placementRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() ||
		in.Epoch == "" || in.PodUID == "" || in.PVCUID == "" || !sha256Pattern.MatchString(in.TokenSHA256) {
		return definition.PlacementRequest{}, errInvalidPlacementBody
	}
	out := definition.PlacementRequest{PodUID: in.PodUID, PVCUID: in.PVCUID, Epoch: in.Epoch, TokenSHA256: in.TokenSHA256}
	if in.Previous != nil {
		p := in.Previous
		if p.PodUID == "" || p.PodUID == in.PodUID ||
			(p.WriterStoppedSHA256 != "" && !sha256Pattern.MatchString(p.WriterStoppedSHA256)) {
			return definition.PlacementRequest{}, errInvalidPlacementBody
		}
		out.Previous = definition.PreviousPlacement{PodUID: p.PodUID, WriterStoppedSHA256: p.WriterStoppedSHA256}
	}
	return out, nil
}
