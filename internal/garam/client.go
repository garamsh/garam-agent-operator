package garam

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// requestTimeout bounds one call to garam. It is what stops a listener that
// accepted the connection and then said nothing from holding a poll open until
// the manager stops.
const requestTimeout = 30 * time.Second

// Client reads this operator's definitions from garam's machine listener and
// claims them. What the caller is scopes both: garam answers the definitions
// naming the certificate's operator and no others.
type Client struct {
	address string
	http    *http.Client
}

// NewClient returns a Client reaching garam's machine listener at address, a
// host and port, over the given TLS configuration.
func NewClient(address string, tlsConfig *tls.Config) *Client {
	return &Client{
		address: address,
		http: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
				// One connection per call. A reused connection performs no
				// handshake, and a handshake is where the certificate on disk
				// is read: kept alive between polls, this client would go on
				// presenting the certificate it started with.
				DisableKeepAlives: true,
			},
		},
	}
}

// ListDefinitions returns the definitions naming this operator, with the agent
// GRN each minted and the claim standing on it where there is one.
func (c *Client) ListDefinitions(ctx context.Context) ([]Definition, error) {
	var answered []definitionPayload
	if err := c.send(ctx, http.MethodGet, "/definitions", nil, &answered); err != nil {
		return nil, fmt.Errorf("list the definitions garam holds for this operator: %w", err)
	}
	definitions := make([]Definition, 0, len(answered))
	for _, payload := range answered {
		definition, err := payload.definition()
		if err != nil {
			return nil, fmt.Errorf("list the definitions garam holds for this operator: %w", err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

// ClaimDefinition binds this operator to the agent a definition minted, which
// is what admits it to that agent's certificate route. An agent garam already
// holds a claim for answers [ErrClaimConflict], which no retry changes.
func (c *Client) ClaimDefinition(ctx context.Context, agent GRN) (Assignment, error) {
	var answered assignmentPayload
	err := c.send(ctx, http.MethodPost, "/definitions/"+url.PathEscape(string(agent))+"/claim", nil, &answered)

	var refused *refusal
	if errors.As(err, &refused) && refused.status == http.StatusConflict {
		return Assignment{}, fmt.Errorf("claim %s: %w", agent, ErrClaimConflict)
	}
	if err != nil {
		return Assignment{}, fmt.Errorf("claim %s: %w", agent, err)
	}
	return answered.assignment()
}

// IssueAgentCertificate asks garam for the credential an agent authenticates
// with. It answers for an agent assigned to this operator at the current epoch,
// which is what the claim bought.
//
// An agent this operator does not hold answers [ErrAgentNotHeld]. The private
// key comes back once and garam keeps none, so what this returns is not
// obtainable again: store it before treating it as obtained.
func (c *Client) IssueAgentCertificate(ctx context.Context, agent GRN) (AgentCredential, error) {
	var answered agentCredentialPayload
	err := c.send(ctx, http.MethodPost, "/agents/"+url.PathEscape(string(agent))+"/certificate", nil, &answered)

	var refused *refusal
	if errors.As(err, &refused) && refused.status == http.StatusForbidden {
		return AgentCredential{}, fmt.Errorf("issue a certificate for %s: %w", agent, ErrAgentNotHeld)
	}
	if err != nil {
		return AgentCredential{}, fmt.Errorf("issue a certificate for %s: %w", agent, err)
	}
	return answered.agentCredential()
}

// ReportProvisioningState tells garam what this operator sees of an agent's pod.
// garam learns it here and nowhere else: it dials no cluster and reads no pod
// (garam@e1e69fd:api/machine.yaml:201-231).
//
// epoch is the epoch this operator holds the agent at, and garam accepts the
// report only at the epoch the assignment is currently on. A later one answers
// [ErrReportStale]; an agent this operator is not assigned to and one that does
// not exist answer [ErrAgentNotHeld], identically, so a caller learns nothing
// from which it got.
//
// The state garam records is a latch it never expires, so what this writes
// stands until it is written again and is evidence of what this operator saw
// rather than evidence that this operator is still there.
func (c *Client) ReportProvisioningState(ctx context.Context, agent GRN, epoch int64, state ProvisioningState) error {
	report := provisioningStateReport{Epoch: epoch, State: string(state)}
	err := c.send(ctx, http.MethodPost, "/agents/"+url.PathEscape(string(agent))+"/provisioning-state", report, nil)

	// Discriminated the way each refusal allows: by kind where garam answers one
	// status for two facts, as RenewIdentity does, and by status where it
	// answers one fact, as IssueAgentCertificate does for this same 403.
	var refused *refusal
	if errors.As(err, &refused) {
		switch {
		case refused.kind == kindFailedPrecondition:
			return fmt.Errorf("report %s at epoch %d: %w", agent, epoch, ErrReportStale)
		case refused.status == http.StatusForbidden:
			return fmt.Errorf("report %s at epoch %d: %w", agent, epoch, ErrAgentNotHeld)
		}
	}
	if err != nil {
		return fmt.Errorf("report %s at epoch %d: %w", agent, epoch, err)
	}
	return nil
}

// garam tells its refusals apart by kind rather than by status: a renewal
// refused as too early and one refused as superseded are both answered 409
// (garam@1150b88:internal/wire/error.go:16), and the two ask for opposite
// things — wait, and mint out of band.
const (
	kindFailedPrecondition = "failed_precondition"
	kindAlreadyExists      = "already_exists"
)

// RenewIdentity replaces the certificate this operator authenticates with, by
// presenting the one it currently holds. The route names no subject: what is
// replaced is what the connection authenticated as.
//
// A renewal garam admits no sooner answers [ErrRenewalTooEarly], and one of a
// certificate it has already replaced answers [ErrCredentialSuperseded].
//
// The answer also carries the authority that signed the certificate and the
// root garam is verified against. Neither is read here: what this operator
// verifies garam against is the deployment's to supply, and moving it is not
// this call's to do.
func (c *Client) RenewIdentity(ctx context.Context) (Credential, error) {
	var answered credentialPayload
	err := c.send(ctx, http.MethodPost, "/identity/certificate", nil, &answered)

	var refused *refusal
	if errors.As(err, &refused) {
		switch refused.kind {
		case kindFailedPrecondition:
			return Credential{}, fmt.Errorf("renew the certificate this operator authenticates with: %w", ErrRenewalTooEarly)
		case kindAlreadyExists:
			return Credential{}, fmt.Errorf("renew the certificate this operator authenticates with: %w", ErrCredentialSuperseded)
		}
	}
	if err != nil {
		return Credential{}, fmt.Errorf("renew the certificate this operator authenticates with: %w", err)
	}
	return answered.credential()
}

// Enroll obtains this operator's first certificate: garam signs the request for
// the operator the token names, and the token is spent
// (garam@b16a896:api/machine.yaml:147-198). It is the one route on this
// listener a caller reaches with no certificate, which is what a caller that
// has none needs.
//
// The request's own signature is checked before anything is sent. The token is
// spent by the attempt whether or not the attempt succeeds, so a request garam
// would refuse costs a registration rather than a retry.
//
// A token garam refuses answers [ErrTokenNotUsable], and nothing retries: one
// token is one attempt. What comes back is not obtainable again — garam
// generated no key and holds none — so store it before treating it as obtained.
func (c *Client) Enroll(ctx context.Context, token string, request CertificateRequest) (EnrolledCertificate, error) {
	if err := request.checkSignature(); err != nil {
		return EnrolledCertificate{}, fmt.Errorf("enroll this operator: %w", err)
	}

	var answered enrolledCertificatePayload
	presented := enrollment{Token: token, CertificateRequestPEM: string(request.RequestPEM)}
	err := c.send(ctx, http.MethodPost, "/enrollment", presented, &answered)

	var refused *refusal
	if errors.As(err, &refused) && refused.status == http.StatusUnauthorized {
		return EnrolledCertificate{}, fmt.Errorf("enroll this operator: %w", ErrTokenNotUsable)
	}
	if err != nil {
		return EnrolledCertificate{}, fmt.Errorf("enroll this operator: %w", err)
	}
	return answered.enrolledCertificate()
}

// send performs one request against the machine listener, carrying body where
// there is one, and decodes what it answered into out.
//
// A nil body sends none and a nil out reads none: a route that records
// something answers 204 and has nothing to decode.
func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("render what this operator is telling garam: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, "https://"+c.address+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	answered, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return refusalFrom(response.StatusCode, answered)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(answered, out); err != nil {
		return fmt.Errorf("decode what garam answered: %w", err)
	}
	return nil
}

// refusal is a status garam answered instead of the resource asked for, with
// the error kind and message it carries where it carries them.
type refusal struct {
	status  int
	kind    string
	message string
}

func (r *refusal) Error() string {
	if r.kind == "" {
		return fmt.Sprintf("garam answered %d", r.status)
	}
	return fmt.Sprintf("garam answered %d %s: %s", r.status, r.kind, r.message)
}

// refusalFrom reads garam's error body where the body is one, and reports the
// status alone where it is not: a refusal is a refusal whatever answered it.
func refusalFrom(status int, body []byte) *refusal {
	answered := struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}{}
	if err := json.Unmarshal(body, &answered); err != nil {
		return &refusal{status: status}
	}
	return &refusal{status: status, kind: answered.Kind, message: answered.Message}
}

// provisioningStateReport is what this operator sends the machine listener about
// an agent's pod. Both fields are required and garam refuses an epoch below 1
// (garam@e1e69fd:api/machine.yaml:617-634).
type provisioningStateReport struct {
	Epoch int64  `json:"epoch"`
	State string `json:"state"`
}

// definitionPayload is one definition as the machine listener answers it.
type definitionPayload struct {
	AgentGRN string            `json:"agentGrn"`
	Values   map[string]string `json:"values"`
	Claim    *claimPayload     `json:"claim"`
}

type claimPayload struct {
	Epoch int64 `json:"epoch"`
}

// toolPinPrefix is the one value-key family this operator reads. The suffix is
// a tool's name and the string beside it is that tool's pin. The name is
// sherlock's own setting name, because this operator has to write that setting
// anyway.
const toolPinPrefix = "tools.pins."

// readValues is where a definition's free-form values stop being free-form.
// garam's answer is a third-party map and this is the one place it is read, so
// what travels on is the typed selection and the names of the keys that were
// not read — never the map, which is what keeps a key this operator has no
// contract for from reaching the workload.
//
// A key the selection does not cover is ignored rather than refused: this
// operator cannot tell a typo from a key a later release understands, and
// refusing would turn either into an agent that never runs.
func readValues(values map[string]string) (ToolSet, []string) {
	var tools ToolSet
	var ignored []string
	for key, value := range values {
		name, found := strings.CutPrefix(key, toolPinPrefix)
		if !found || name == "" {
			ignored = append(ignored, key)
			continue
		}
		if tools.Pins == nil {
			tools.Pins = make(map[string]string)
		}
		tools.Pins[name] = value
	}
	// Sorted, so that what is reported about one definition does not change
	// between passes that read the same answer.
	slices.Sort(ignored)

	return tools, ignored
}

func (p definitionPayload) definition() (Definition, error) {
	if p.AgentGRN == "" {
		return Definition{}, errors.New("a definition garam answered names no agent")
	}
	definition := Definition{Agent: GRN(p.AgentGRN)}
	definition.Tools, definition.Ignored = readValues(p.Values)
	if p.Claim != nil {
		if p.Claim.Epoch < 1 {
			return Definition{}, fmt.Errorf("a definition garam answered claims %s at no epoch", p.AgentGRN)
		}
		definition.Claim = &Claim{Epoch: p.Claim.Epoch}
	}
	return definition, nil
}

// assignmentPayload is an assignment as the machine listener answers it.
type assignmentPayload struct {
	GRN      string `json:"grn"`
	Operator string `json:"operator"`
	Epoch    int64  `json:"epoch"`
}

func (p assignmentPayload) assignment() (Assignment, error) {
	if p.GRN == "" || p.Operator == "" {
		return Assignment{}, errors.New("an assignment garam answered names no agent or no operator")
	}
	return Assignment{Agent: GRN(p.GRN), Operator: GRN(p.Operator), Epoch: p.Epoch}, nil
}

// credentialPayload is a certificate and its key as the machine listener
// answers them.
type credentialPayload struct {
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem"`
}

func (p credentialPayload) credential() (Credential, error) {
	if p.CertificatePEM == "" || p.PrivateKeyPEM == "" {
		return Credential{}, errors.New("a renewal garam answered carries no certificate or no key")
	}
	return Credential{CertificatePEM: []byte(p.CertificatePEM), KeyPEM: []byte(p.PrivateKeyPEM)}, nil
}

// agentCredentialPayload is an agent's certificate as the machine listener
// answers it.
type agentCredentialPayload struct {
	CertificatePEM string    `json:"certificatePem"`
	PrivateKeyPEM  string    `json:"privateKeyPem"`
	IssuerPEM      string    `json:"issuerPem"`
	ServerRootPEM  string    `json:"serverRootPem"`
	NotAfter       time.Time `json:"notAfter"`
}

func (p agentCredentialPayload) agentCredential() (AgentCredential, error) {
	if p.CertificatePEM == "" || p.PrivateKeyPEM == "" || p.IssuerPEM == "" || p.ServerRootPEM == "" {
		return AgentCredential{}, errors.New("a certificate garam issued an agent is missing one of its four PEMs")
	}
	if p.NotAfter.IsZero() {
		return AgentCredential{}, errors.New("a certificate garam issued an agent carries no expiry")
	}
	return AgentCredential{
		CertificatePEM: []byte(p.CertificatePEM),
		KeyPEM:         []byte(p.PrivateKeyPEM),
		IssuerPEM:      []byte(p.IssuerPEM),
		ServerRootPEM:  []byte(p.ServerRootPEM),
		NotAfter:       p.NotAfter,
	}, nil
}

// enrollment is what this operator presents to enroll: the token it was given
// and the request over the key it generated. The token lives in this value for
// the length of the call and in nothing that outlives it.
type enrollment struct {
	Token                 string `json:"token"`
	CertificateRequestPEM string `json:"certificateRequestPem"`
}

// enrolledCertificatePayload is the certificate an enrollment answers. garam
// answers no private key here, because it generated none
// (garam@b16a896:api/machine.yaml:813-820).
type enrolledCertificatePayload struct {
	GRN            string    `json:"grn"`
	CertificatePEM string    `json:"certificatePem"`
	ServerRootPEM  string    `json:"serverRootPem"`
	NotAfter       time.Time `json:"notAfter"`
}

// enrolledCertificate reads what an enrollment answered, refusing only an
// answer with no certificate in it.
//
// It is stricter nowhere else on purpose, where [agentCredentialPayload] checks
// every field: an agent's certificate can be asked for again and an enrollment
// cannot, so discarding one over a field this operator does not act on would
// cost the certificate and the token together. The garam server root is carried
// to be compared and not to be stored, and the issuer that signed the
// certificate is not carried at all: that authority signs no listener, and what
// verifies garam is the deployment's to supply.
func (p enrolledCertificatePayload) enrolledCertificate() (EnrolledCertificate, error) {
	if p.CertificatePEM == "" {
		return EnrolledCertificate{}, errors.New("an enrollment garam answered carries no certificate")
	}
	return EnrolledCertificate{
		Operator:       GRN(p.GRN),
		CertificatePEM: []byte(p.CertificatePEM),
		ServerRootPEM:  []byte(p.ServerRootPEM),
		NotAfter:       p.NotAfter,
	}, nil
}
