package distribution

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
)

// controller is the caller of a controller route: the operator GRN its leaf certificate names,
// and that leaf exactly as presented.
type controller struct {
	grn     string
	leafPEM []byte
}

// authenticate reads the controller from the request's client certificate and has garam prove it
// for this request. A proof is never kept past the request it was obtained for.
func (s *server) authenticate(ctx context.Context, r *http.Request) (controller, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return controller{}, ErrNoCertificate
	}
	leaf := r.TLS.PeerCertificates[0]
	if len(leaf.URIs) != 1 {
		return controller{}, ErrNoCertificate
	}
	c := controller{
		grn:     leaf.URIs[0].String(),
		leafPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}),
	}
	proof, err := s.prover.Prove(ctx, c.grn, c.leafPEM, "")
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
	proof, err := s.prover.Prove(ctx, c.grn, c.leafPEM, agent)
	if err != nil {
		return "", err
	}
	if proof.Operator != c.grn || proof.Agent == nil || proof.Agent.GRN != agent {
		return "", ErrNotProved
	}
	return proof.Agent.Epoch, nil
}
