package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const (
	// maxRequestBytes bounds a request's body on these routes.
	maxRequestBytes = 4 << 10
	// placementScheme is the Authorization scheme the placement token is presented under.
	placementScheme = "Garam-Placement "
	// credentialCurrent and generationCurrent are introspectAgentExecution's answers that admit.
	credentialCurrent = "current"
	generationCurrent = "current"
)

var (
	// requestIDPattern is the request identifier garam takes.
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)
	// generationPattern is a runtime's start nonce: 128 bits as 32 lowercase hex digits.
	generationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// activationRequest is the body of an activation, each field a string.
type activationRequest struct {
	RequestID      string `json:"requestId"`
	Epoch          string `json:"epoch"`
	Generation     string `json:"generation"`
	ConfigRevision string `json:"configRevision"`
}

// activationResponse is garam's activation with the binding it was asked for.
type activationResponse struct {
	ActivationID   string `json:"activationId"`
	TokenVersion   int    `json:"tokenVersion"`
	Token          string `json:"token"`
	GRN            string `json:"grn"`
	Epoch          string `json:"epoch"`
	Generation     string `json:"generation"`
	ConfigRevision string `json:"configRevision"`
}

// activate activates the runtime generation the agent's adapter asks for, on the agent's current
// placement. Placement, credential and authority are checked on every attempt, and an attempt
// sends garam the request stored under its identifier, so a retry after a lost answer or a restart
// is the same activation at a higher token version, never a new one.
func (s *server) activate(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		s.respondError(w, errInvalidRequest)
		return
	}
	leafPEM, err := agentLeaf(r, agent)
	if err != nil {
		s.respondError(w, err)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), placementScheme)
	if !ok || token == "" {
		s.respondError(w, errUnauthenticated)
		return
	}
	in, err := parseActivation(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	placement, err := s.currentPlacement(r.Context(), agent, token, in.Epoch)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in.PlacementPodUID = placement.Request.PodUID

	var (
		answer  Activation
		created bool
	)
	err = s.definitions.WithActivationLock(r.Context(), definition.GRN(agent), func(ctx context.Context) error {
		stored, err := s.definitions.PrepareActivation(ctx, definition.GRN(agent), in)
		if err != nil {
			return err
		}
		if err := s.admit(ctx, agent, leafPEM, in.Epoch, placement); err != nil {
			return err
		}
		answer, created, err = s.callActivate(ctx, agent, stored, leafPEM)
		if err != nil {
			return err
		}
		return s.definitions.RecordActivation(ctx, definition.GRN(agent), in.RequestID, answer.ActivationID)
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, activationResponse{
		ActivationID: answer.ActivationID, TokenVersion: answer.TokenVersion, Token: answer.Token,
		GRN: answer.GRN, Epoch: answer.Epoch, Generation: answer.Generation, ConfigRevision: in.ConfigRevision.String(),
	})
}

// currentPlacement is the agent's current placement, once the token presented is its token and
// the epoch asked for is its epoch.
func (s *server) currentPlacement(ctx context.Context, agent, token, epoch string) (definition.Placement, error) {
	placement, err := s.definitions.CurrentPlacement(ctx, definition.GRN(agent))
	if errors.Is(err, definition.ErrNotFound) {
		return definition.Placement{}, errPlacementNotCurrent
	}
	if err != nil {
		return definition.Placement{}, err
	}
	digest := sha256.Sum256([]byte(token))
	if hex.EncodeToString(digest[:]) != placement.Request.TokenSHA256 {
		return definition.Placement{}, errPlacementNotCurrent
	}
	if epoch != placement.Request.Epoch {
		return definition.Placement{}, errEpochSuperseded
	}
	return placement, nil
}

// admit has garam prove, for this attempt, that the leaf is the agent's current credential on the
// placement's controller at epoch, and that the controller the placement was registered by is still
// the agent's, proved over the leaf stored with the placement. The generation is not asked: it is
// the one being activated.
func (s *server) admit(ctx context.Context, agent string, leafPEM []byte, epoch string, placement definition.Placement) error {
	execution, err := s.garam.Introspect(ctx, agent, leafPEM, "")
	if err != nil {
		return refusalOf(err, errNotAuthorized)
	}
	switch {
	case execution.Credential != credentialCurrent:
		return errCredentialFenced
	case execution.GRN != agent || execution.Assignee != placement.Controller:
		return errPlacementNotCurrent
	case execution.Epoch != epoch:
		return errEpochSuperseded
	}
	stored := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: placement.LeafDER})
	proof, err := s.garam.ProveController(ctx, placement.Controller, stored, agent)
	if err != nil {
		return refusalOf(err, errNotAuthorized)
	}
	if proof.Operator != placement.Controller || proof.Agent != agent || proof.Epoch != epoch {
		return errEpochSuperseded
	}
	return nil
}

// callActivate sends garam the stored request under this attempt's leaf, and checks that garam
// answered the binding it was asked for.
func (s *server) callActivate(ctx context.Context, agent string, stored definition.Activation, leafPEM []byte) (Activation, bool, error) {
	answer, created, err := s.garam.Activate(ctx, agent, ActivationCall{
		RequestID: stored.Request.RequestID, Epoch: stored.Request.Epoch, Generation: stored.Request.Generation,
		ReplacesActivationID: stored.ReplacesActivationID, OperationRef: stored.OperationRef, CertificatePEM: leafPEM,
	})
	if err != nil {
		return Activation{}, false, refusalOf(err, errNotAuthorized)
	}
	if answer.GRN != agent || answer.Epoch != stored.Request.Epoch || answer.Generation != stored.Request.Generation ||
		answer.ActivationID == "" || answer.Token == "" || answer.TokenVersion < 1 {
		return Activation{}, false, errors.New("garam answered an activation of another binding")
	}
	return answer, created, nil
}

// refusalOf is the answer to a refusal garam gave, with forbidden the one its refused authority is
// answered with on this route.
func refusalOf(err error, forbidden *refusal) error {
	switch {
	case errors.Is(err, ErrUndecided):
		return errUndecided
	case errors.Is(err, ErrNotAuthorized):
		return forbidden
	case errors.Is(err, ErrCredentialRefused):
		return errCredentialFenced
	case errors.Is(err, ErrActivationSuperseded):
		return errActivationSuperseded
	}
	return err
}

// parseActivation reads exactly one activation request, refusing a field it does not know.
func parseActivation(body []byte) (definition.ActivationRequest, error) {
	var in activationRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() ||
		!requestIDPattern.MatchString(in.RequestID) || in.Epoch == "" || !generationPattern.MatchString(in.Generation) {
		return definition.ActivationRequest{}, errInvalidRequest
	}
	revision, err := definition.ParseRevision(in.ConfigRevision)
	if err != nil {
		return definition.ActivationRequest{}, errInvalidRequest
	}
	return definition.ActivationRequest{
		RequestID: in.RequestID, Epoch: in.Epoch, Generation: in.Generation, ConfigRevision: revision,
	}, nil
}
