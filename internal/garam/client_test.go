package garam_test

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// sampleAgent is an agent GRN in the shape garam mints them.
const sampleAgent = garam.GRN("grn:acme:default:agent:9f2ac1b40d8e7a35")

// answerJSON writes status and body the way the machine listener does.
func answerJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestClientListDefinitionsReadsTheAgentToolSetAndClaimGaramAnswers(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK, `[
			{"agentGrn": "`+string(sampleAgent)+`", "values": {
				"tools.pins.message_send": "sha256:aa",
				"tools.pins.files": "sha256:bb"
			}},
			{"agentGrn": "grn:acme:default:agent:0a1b2c3d4e5f6071", "values": {}, "claim": {"epoch": 3}}
		]`)
	})

	definitions, err := garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(definitions).To(Equal([]garam.Definition{
		{Agent: sampleAgent, Tools: garam.ToolSet{Pins: map[string]string{
			"message_send": "sha256:aa",
			"files":        "sha256:bb",
		}}, Claim: nil},
		{Agent: "grn:acme:default:agent:0a1b2c3d4e5f6071", Claim: &garam.Claim{Epoch: 3}},
	}))
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].method).To(Equal(http.MethodGet))
	g.Expect(stub.requests()[0].path).To(Equal("/definitions"))
}

func TestClientListDefinitionsCarriesTheKeysItReadsAndNamesTheRestUnread(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK, `[{"agentGrn": "`+string(sampleAgent)+`", "values": {
			"tools.pins.files": "sha256:bb",
			"model": "haiku",
			"image": "example.com/somebody-elses:latest",
			"tools.pins": "sha256:cc",
			"tools.pins.": "sha256:dd"
		}}]`)
	})

	definitions, err := garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definitions).To(HaveLen(1))

	// The control: the key family this operator holds a contract for is read, so
	// what is left below is unread for want of a contract and not because the
	// whole answer was dropped.
	g.Expect(definitions[0].Tools.Pins).To(Equal(map[string]string{"files": "sha256:bb"}))

	// Sorted, and names only: a key this operator cannot act on says nothing
	// worth carrying but its own name, and an image key is the one this closed
	// set exists to leave outside.
	g.Expect(definitions[0].Ignored).To(Equal([]string{"image", "model", "tools.pins", "tools.pins."}))
}

func TestClientListDefinitionsIgnoresNothingWhereEveryKeyIsRead(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK,
			`[{"agentGrn": "`+string(sampleAgent)+`", "values": {"tools.pins.files": "sha256:bb"}}]`)
	})

	definitions, err := garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definitions).To(HaveLen(1))
	g.Expect(definitions[0].Ignored).To(BeEmpty())
}

func TestClientListDefinitionsDeclaresNoToolSetWhereTheAnswerCarriesNoPin(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK,
			`[{"agentGrn": "`+string(sampleAgent)+`", "values": {"model": "haiku"}}]`)
	})

	definitions, err := garam.NewClient(stub.address(), trustedBy(t, stub)).ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definitions).To(HaveLen(1))

	// Nil and not empty: a definition declaring no pin is not one declaring an
	// empty set, and only the first leaves nothing placed.
	g.Expect(definitions[0].Tools.Pins).To(BeNil())
}

func TestClientListDefinitionsRefusesAnAnswerNamingNoAgent(t *testing.T) {
	g := NewWithT(t)
	answer := `[{"agentGrn": "` + string(sampleAgent) + `", "values": {}}]`
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK, answer)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	// The answer that is read, so that the refusal below is the missing agent
	// and not the shape of the payload around it.
	_, err := client.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	answer = `[{"values": {}}]`
	_, err = client.ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("names no agent")))
}

func TestClientClaimDefinitionBindsTheAgentItNames(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusCreated, `{
			"grn": "`+string(sampleAgent)+`",
			"operator": "grn:acme:default:operator:one",
			"epoch": 1
		}`)
	})

	assignment, err := garam.NewClient(stub.address(), trustedBy(t, stub)).
		ClaimDefinition(context.Background(), sampleAgent)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(assignment).To(Equal(garam.Assignment{
		Agent:    sampleAgent,
		Operator: "grn:acme:default:operator:one",
		Epoch:    1,
	}))
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].method).To(Equal(http.MethodPost))
	g.Expect(stub.requests()[0].path).To(Equal("/definitions/" + string(sampleAgent) + "/claim"))
}

func TestClientClaimDefinitionReportsAConflictAndNoOtherRefusalAsOne(t *testing.T) {
	g := NewWithT(t)
	status := http.StatusConflict
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, status, `{"kind": "already_exists", "message": "definition already claimed"}`)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	_, err := client.ClaimDefinition(context.Background(), sampleAgent)
	g.Expect(err).To(MatchError(garam.ErrClaimConflict))

	// A refusal that is not a conflict, so that the match above is the status
	// and not every refusal reading as one.
	status = http.StatusServiceUnavailable
	_, err = client.ClaimDefinition(context.Background(), sampleAgent)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).NotTo(MatchError(garam.ErrClaimConflict))
}

// agentCertificateAnswer is the shape garam answers an issued agent
// certificate in, with all six fields its schema requires
// (garam@5130ca9:api/machine.yaml:455).
const agentCertificateAnswer = `{
	"grn": "` + string(sampleAgent) + `",
	"certificatePem": "a certificate",
	"privateKeyPem": "a key",
	"issuerPem": "the authority that signed it",
	"serverRootPem": "the root garam is verified against",
	"notAfter": "2026-08-26T15:01:43Z"
}`

func TestClientIssueAgentCertificateReadsEveryPartOfTheAnswer(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusCreated, agentCertificateAnswer)
	})

	credential, err := garam.NewClient(stub.address(), trustedBy(t, stub)).
		IssueAgentCertificate(context.Background(), sampleAgent)
	g.Expect(err).NotTo(HaveOccurred())

	// The issuer and the server root are two different certificates and the
	// wrong one cannot reach garam at all, so each is read into the field that
	// says which it is.
	g.Expect(credential).To(Equal(garam.AgentCredential{
		CertificatePEM: []byte("a certificate"),
		KeyPEM:         []byte("a key"),
		IssuerPEM:      []byte("the authority that signed it"),
		ServerRootPEM:  []byte("the root garam is verified against"),
		NotAfter:       time.Date(2026, 8, 26, 15, 1, 43, 0, time.UTC),
	}))
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].method).To(Equal(http.MethodPost))

	// The agent's route and not /identity/certificate, which renews the
	// caller's own identity and names no agent.
	g.Expect(stub.requests()[0].path).To(Equal("/agents/" + string(sampleAgent) + "/certificate"))
}

func TestClientIssueAgentCertificateReportsAForbiddenAndNoOtherRefusalAsNotHeld(t *testing.T) {
	g := NewWithT(t)
	status := http.StatusForbidden
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, status, `{"kind": "permission_denied", "message": "agent is not assigned to this operator"}`)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	_, err := client.IssueAgentCertificate(context.Background(), sampleAgent)
	g.Expect(err).To(MatchError(garam.ErrAgentNotHeld))

	// A refusal that is not a 403, so that the match above is the status and
	// not every refusal reading as one.
	status = http.StatusServiceUnavailable
	_, err = client.IssueAgentCertificate(context.Background(), sampleAgent)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).NotTo(MatchError(garam.ErrAgentNotHeld))
}

func TestClientIssueAgentCertificateRefusesAnIncompleteAnswer(t *testing.T) {
	g := NewWithT(t)
	answer := agentCertificateAnswer
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusCreated, answer)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	// The whole answer, so that each refusal below is the field taken out of it
	// and not the shape of the payload around it.
	_, err := client.IssueAgentCertificate(context.Background(), sampleAgent)
	g.Expect(err).NotTo(HaveOccurred())

	for _, field := range []string{"certificatePem", "privateKeyPem", "issuerPem", "serverRootPem"} {
		answer = strings.Replace(agentCertificateAnswer, field, "not"+field, 1)
		_, err = client.IssueAgentCertificate(context.Background(), sampleAgent)
		g.Expect(err).To(MatchError(ContainSubstring("four PEMs")), "%s was not required", field)
	}

	answer = strings.Replace(agentCertificateAnswer, "notAfter", "notNotAfter", 1)
	_, err = client.IssueAgentCertificate(context.Background(), sampleAgent)
	g.Expect(err).To(MatchError(ContainSubstring("no expiry")))
}

// trustedBy is the TLS configuration verifying stub and nothing else.
func trustedBy(t *testing.T, stub *stubListener) *tls.Config {
	t.Helper()

	certificateFile, keyFile := writeIdentity(t, t.TempDir(), "operator")
	tlsConfig, err := garam.MutualTLS(certificateFile, keyFile, stub.trustFile)
	if err != nil {
		t.Fatalf("configure mutual TLS against the stub listener: %v", err)
	}
	return tlsConfig
}

// TestClientListDefinitionsRefusesAClaimAtNoEpoch keeps a claim and the epoch
// it stands at together. A report to garam carries that epoch, so a claim
// answered without one is a definition this operator could construct and never
// report on — a gap that would surface a poll interval later and far from here.
func TestClientListDefinitionsRefusesAClaimAtNoEpoch(t *testing.T) {
	g := NewWithT(t)
	answer := `[{"agentGrn": "` + string(sampleAgent) + `", "values": {}, "claim": {"epoch": 4}}]`
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK, answer)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	// The answer that is read, so that the refusal below is the missing epoch
	// and not the claim being present at all.
	definitions, err := client.ListDefinitions(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(definitions[0].Claim).To(Equal(&garam.Claim{Epoch: 4}))

	answer = `[{"agentGrn": "` + string(sampleAgent) + `", "values": {}, "claim": {}}]`
	_, err = client.ListDefinitions(context.Background())
	g.Expect(err).To(MatchError(ContainSubstring("at no epoch")))
}

func TestClientReportProvisioningStateSendsTheEpochAndTheStateItObserved(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	err := garam.NewClient(stub.address(), trustedBy(t, stub)).
		ReportProvisioningState(context.Background(), sampleAgent, 7, garam.StateReady)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].method).To(Equal(http.MethodPost))
	g.Expect(stub.requests()[0].path).To(Equal("/agents/" + string(sampleAgent) + "/provisioning-state"))
	g.Expect(stub.requests()[0].body).To(MatchJSON(`{"epoch": 7, "state": "ready"}`))
}

// TestClientReportProvisioningStateReportsAStaleEpochAsStale is what makes the
// epoch on a report worth carrying. garam refuses a report at an epoch the
// assignment has moved past, and a caller that could not tell that refusal from
// any other would have no way to know its record of the agent is behind.
func TestClientReportProvisioningStateReportsAStaleEpochAsStale(t *testing.T) {
	g := NewWithT(t)
	stale := true
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		if !stale {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		answerJSON(w, http.StatusConflict,
			`{"kind": "failed_precondition", "message": "the assignment is at a later epoch"}`)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	err := client.ReportProvisioningState(context.Background(), sampleAgent, 1, garam.StateReady)
	g.Expect(err).To(MatchError(garam.ErrReportStale))

	// The same call against a listener that accepts it, so that what the
	// refusal above rests on is the answer and not the request.
	stale = false
	g.Expect(client.ReportProvisioningState(context.Background(), sampleAgent, 1, garam.StateReady)).To(Succeed())
}

// TestClientReportProvisioningStateReportsAnAgentItDoesNotHoldAsNotHeld pairs
// with the certificate route, which garam refuses the same way. An agent
// assigned elsewhere and one that does not exist answer identically here, so
// this maps both to the one error that says so.
func TestClientReportProvisioningStateReportsAnAgentItDoesNotHoldAsNotHeld(t *testing.T) {
	g := NewWithT(t)
	refuse := true
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		if !refuse {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		answerJSON(w, http.StatusForbidden,
			`{"kind": "permission_denied", "message": "agent is not assigned to this operator"}`)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	err := client.ReportProvisioningState(context.Background(), sampleAgent, 7, garam.StateProvisioned)
	g.Expect(err).To(MatchError(garam.ErrAgentNotHeld))
	g.Expect(err).NotTo(MatchError(garam.ErrReportStale))

	refuse = false
	g.Expect(client.ReportProvisioningState(context.Background(), sampleAgent, 7, garam.StateProvisioned)).To(Succeed())
}
