package distribution

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// controllerHeader names the GRN the controller presents its token under (ADR 0072).
const controllerHeader = "Controller-Grn"

// operatorToken is the form of a garam operator token (garam@9ba35c3 api/machine.yaml
// ControllerIntrospection.token); a request presenting another is refused before garam is asked.
var operatorToken = regexp.MustCompile(`^garam_ot_[0-9a-f]{64}$`)

// controller is the caller of a controller route: the operator GRN it named, and the token it
// presented, exactly as presented. The token is forwarded to garam and never logged.
type controller struct {
	grn   string
	token string
}

// authenticate reads the controller from the request's bearer token and the GRN beside it, and has
// garam prove them for this request. A proof is never kept past the request it was obtained for.
func (s *server) authenticate(ctx context.Context, r *http.Request) (controller, error) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	grn := r.Header.Get(controllerHeader)
	if !found || !strings.EqualFold(scheme, "Bearer") || !operatorToken.MatchString(token) || grn == "" {
		return controller{}, ErrNoToken
	}
	c := controller{grn: grn, token: token}
	proof, err := s.prover.Prove(ctx, c.grn, c.token, "")
	if err != nil {
		return controller{}, err
	}
	if proof.Operator != c.grn {
		return controller{}, ErrAnotherOperator
	}
	return c, nil
}

// placed has garam prove, for one decision, that agent is assigned to c under epoch. A refusal or
// another epoch is false; only an undecided or failed proof is an error.
func (s *server) placed(ctx context.Context, c controller, agent, epoch string) (bool, error) {
	proved, err := s.provedEpoch(ctx, c, agent)
	if errors.Is(err, ErrNotProved) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return proved == epoch, nil
}

// provedEpoch has garam prove, for one decision, that agent is assigned to c, and returns the
// epoch it proved. A refusal, or a proof naming another operator or agent, is ErrNotProved.
func (s *server) provedEpoch(ctx context.Context, c controller, agent string) (string, error) {
	proof, err := s.prover.Prove(ctx, c.grn, c.token, agent)
	if err != nil {
		return "", err
	}
	if proof.Operator != c.grn || proof.Agent == nil || proof.Agent.GRN != agent {
		return "", ErrNotProved
	}
	return proof.Agent.Epoch, nil
}
