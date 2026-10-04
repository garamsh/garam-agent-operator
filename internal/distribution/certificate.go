package distribution

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const (
	// maxCertificateRequestBytes bounds a certificate request's body.
	maxCertificateRequestBytes = 8 << 10
	// maxCSRBytes is the largest certificate request PEM garam takes.
	maxCSRBytes = 4096
)

// requestIDPattern is the request identifier garam takes.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

// certificateRequest is the body of a request for an agent's first certificate.
type certificateRequest struct {
	RequestID             string `json:"requestId"`
	Epoch                 string `json:"epoch"`
	CertificateRequestPEM string `json:"certificateRequestPem"`
}

// certificateResponse is the agent's first certificate as stored: what garam issued, the authority
// that signed it, and the garam server root. It carries no private key.
type certificateResponse struct {
	Agent          string `json:"agent"`
	Epoch          string `json:"epoch"`
	CertificatePEM string `json:"certificatePem"`
	IssuerPEM      string `json:"issuerPem"`
	ServerRootPEM  string `json:"serverRootPem"`
	NotAfter       string `json:"notAfter"`
}

var (
	// errInvalidCertificateBody is returned for a body that is not one certificate request over
	// one PKCS#10 PEM, at most 4 KiB, proving possession of an ECDSA P-256 key.
	errInvalidCertificateBody = errors.New("request body is not a certificate request over an ECDSA P-256 key")

	// errEpochSuperseded is returned when garam proves the agent placed on the controller under
	// another epoch than the request's, or than its latest revision recorded.
	errEpochSuperseded = errors.New("the agent's assignment epoch is not the one requested")
)

// requestCertificate asks for an agent's first certificate over the key in the controller's
// certificate request. The controller is proved per request, and the agent's placement on it per
// decision, before anything is stored.
func (s *server) requestCertificate(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCertificateRequestBytes))
	if err != nil {
		s.respondError(w, errInvalidCertificateBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	in, err := parseCertificateRequest(body)
	if err != nil {
		s.respondError(w, err)
		return
	}
	latest, err := s.definitions.GetDefinition(r.Context(), definition.GRN(agent))
	if err != nil {
		s.respondError(w, err)
		return
	}
	if latest.Assignment == nil || latest.Assignment.Operator != c.grn {
		s.respondError(w, errNotPlaced)
		return
	}
	proved, err := s.provedEpoch(r.Context(), c, agent)
	if errors.Is(err, ErrNotProved) {
		s.respondError(w, errNotPlaced)
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	if proved != in.Epoch || proved != latest.Assignment.Epoch {
		s.respondError(w, errEpochSuperseded)
		return
	}
	stored, first, err := s.definitions.RequestInitialCertificate(r.Context(), definition.InitialCertificateInput{
		Agent: definition.GRN(agent), Controller: c.grn, Request: in,
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	status := http.StatusOK
	if first {
		status = http.StatusCreated
	}
	writeJSON(w, status, certificateResponse{
		Agent: agent, Epoch: stored.Request.Epoch,
		CertificatePEM: stored.Issued.CertificatePEM, IssuerPEM: stored.Issued.IssuerPEM,
		ServerRootPEM: stored.Issued.ServerRootPEM, NotAfter: stored.Issued.NotAfter.UTC().Format(time.RFC3339),
	})
}

// parseCertificateRequest reads exactly one certificate request, refusing a field it does not
// know, and a certificate request garam would not sign over: anything but one PEM-encoded PKCS#10
// request of at most 4 KiB, whose signature proves possession of an ECDSA P-256 key.
func parseCertificateRequest(body []byte) (definition.CertificateRequest, error) {
	var in certificateRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() ||
		!requestIDPattern.MatchString(in.RequestID) || in.Epoch == "" || len(in.CertificateRequestPEM) > maxCSRBytes {
		return definition.CertificateRequest{}, errInvalidCertificateBody
	}
	block, rest := pem.Decode([]byte(in.CertificateRequestPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return definition.CertificateRequest{}, errInvalidCertificateBody
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return definition.CertificateRequest{}, errInvalidCertificateBody
	}
	if key, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || key.Curve != elliptic.P256() {
		return definition.CertificateRequest{}, errInvalidCertificateBody
	}
	return definition.CertificateRequest{RequestID: in.RequestID, Epoch: in.Epoch, CSRPEM: in.CertificateRequestPEM}, nil
}
