package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/garammachine"
)

// Garam asks garam's machine listener for a managed agent's first certificate through
// issueInitialCertificate (garam@7ca51b9, api/machine.yaml), under managed-enrollment.v1.
type Garam struct {
	machine *garammachine.Client
}

var _ definition.Issuer = (*Garam)(nil)

// NewGaram returns a Garam calling garam through machine.
func NewGaram(machine *garammachine.Client) *Garam {
	return &Garam{machine: machine}
}

// refusals is the class of each status garam answers a definite refusal with.
var refusals = map[int]definition.Refusal{
	http.StatusForbidden:           definition.RefusalForbidden,
	http.StatusNotFound:            definition.RefusalForbidden,
	http.StatusConflict:            definition.RefusalConflict,
	http.StatusUnprocessableEntity: definition.RefusalInvalid,
}

// Issue sends the request. garam answers one request with its one stored result however often it
// is sent, so an attempt left undecided is sent again by the machine client within its bound;
// after that, the outcome is unknown.
func (g *Garam) Issue(ctx context.Context, i definition.Issuance) (definition.IssuedCertificate, error) {
	answer, err := g.machine.Post(ctx, garammachine.ManagedEnrollment,
		"/agents/"+url.PathEscape(string(i.Agent))+"/initial-certificate", struct {
			RequestID             string `json:"requestId"`
			Epoch                 string `json:"epoch"`
			CertificateRequestPEM string `json:"certificateRequestPem"`
			OperationRef          string `json:"operationRef"`
		}{i.Request.RequestID, i.Request.Epoch, i.Request.CSRPEM, i.OperationRef})
	var foreign *garammachine.ContractError
	switch {
	case errors.Is(err, garammachine.ErrUndecided):
		return definition.IssuedCertificate{}, fmt.Errorf("%w: %v", definition.ErrIssuanceUndecided, err)
	case errors.As(err, &foreign):
		return definition.IssuedCertificate{}, fmt.Errorf("%w: %v", definition.ErrGaramContractUnsupported, err)
	}
	if err != nil {
		return definition.IssuedCertificate{}, fmt.Errorf("issue initial certificate: %v", err)
	}
	if refusal, ok := refusals[answer.Status]; ok {
		var refused struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(answer.Body, &refused); err != nil || refused.Kind == "" {
			return definition.IssuedCertificate{}, fmt.Errorf("decode refusal %d: %q", answer.Status, garammachine.FirstLine(answer.Body))
		}
		return definition.IssuedCertificate{}, &definition.IssuanceRefusedError{
			Refusal: refusal, Kind: refused.Kind, Message: refused.Message,
		}
	}
	if answer.Status != http.StatusCreated && answer.Status != http.StatusOK {
		return definition.IssuedCertificate{}, fmt.Errorf("issue initial certificate answered %d: %s",
			answer.Status, garammachine.FirstLine(answer.Body))
	}
	var issued struct {
		CertificatePEM string `json:"certificatePem"`
		IssuerPEM      string `json:"issuerPem"`
		ServerRootPEM  string `json:"serverRootPem"`
		NotAfter       string `json:"notAfter"`
	}
	if err := json.Unmarshal(answer.Body, &issued); err != nil ||
		issued.CertificatePEM == "" || issued.IssuerPEM == "" || issued.ServerRootPEM == "" {
		return definition.IssuedCertificate{}, fmt.Errorf("decode initial certificate: %q", garammachine.FirstLine(answer.Body))
	}
	notAfter, err := time.Parse(time.RFC3339, issued.NotAfter)
	if err != nil {
		return definition.IssuedCertificate{}, fmt.Errorf("decode initial certificate: notAfter %q", issued.NotAfter)
	}
	return definition.IssuedCertificate{
		CertificatePEM: issued.CertificatePEM, IssuerPEM: issued.IssuerPEM,
		ServerRootPEM: issued.ServerRootPEM, NotAfter: notAfter.UTC().Truncate(time.Second),
	}, nil
}
