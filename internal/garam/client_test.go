package garam_test

import (
	"context"
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

// sampleToken is an agent token in garam's form; agentTokenAnswer is the shape
// garam answers a minted one in (garam@afde8b8 api/machine.yaml AgentToken).
const (
	sampleToken      = "garam_at_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	agentTokenAnswer = `{
	"token": "` + sampleToken + `",
	"grn": "` + string(sampleAgent) + `",
	"epoch": 4,
	"expiresAt": "2026-10-14T15:01:43Z",
	"serverRootPem": "the root garam is verified against"
}`
)

// answerMint answers status and body under machine-token.v1, as mintAgentToken
// answers every request.
func answerMint(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Garam-Contract-Version", "machine-token.v1")
	answerJSON(w, status, body)
}

func TestClientMintAgentTokenReadsEveryPartOfTheAnswer(t *testing.T) {
	g := NewWithT(t)
	var contract string
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		contract = r.Header.Get("Garam-Contract-Version")
		answerMint(w, http.StatusCreated, agentTokenAnswer)
	})

	token, err := garam.NewClient(stub.address(), trustedBy(t, stub)).
		MintAgentToken(context.Background(), sampleAgent, "")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(token).To(Equal(garam.AgentToken{
		Token:         sampleToken,
		Epoch:         4,
		ExpiresAt:     time.Date(2026, 10, 14, 15, 1, 43, 0, time.UTC),
		ServerRootPEM: []byte("the root garam is verified against"),
	}))
	g.Expect(contract).To(Equal("machine-token.v1"))
	g.Expect(stub.requests()).To(HaveLen(1))
	g.Expect(stub.requests()[0].method).To(Equal(http.MethodPost))
	g.Expect(stub.requests()[0].path).To(Equal("/agents/" + string(sampleAgent) + "/tokens"))
	// No audience asks for the token garam itself accepts.
	g.Expect(stub.requests()[0].body).To(MatchJSON(`{}`))
}

func TestClientMintAgentTokenAsksForTheAudienceAndRefusesATokenOfAnother(t *testing.T) {
	g := NewWithT(t)
	const control = "grn:acme:default:operator:control"
	audience := control
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerMint(w, http.StatusCreated, strings.Replace(agentTokenAnswer, `"epoch"`, `"audience": "`+audience+`", "epoch"`, 1))
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	_, err := client.MintAgentToken(context.Background(), sampleAgent, control)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(stub.requests()[0].body).To(MatchJSON(`{"audience": "` + control + `"}`))

	// A token minted for another audience is not the one asked for.
	audience = "grn:acme:default:operator:other"
	_, err = client.MintAgentToken(context.Background(), sampleAgent, control)
	g.Expect(err).To(MatchError(ContainSubstring("not " + string(sampleAgent))))
}

func TestClientMintAgentTokenReportsAForbiddenAndNoOtherRefusalAsNotHeld(t *testing.T) {
	g := NewWithT(t)
	status := http.StatusForbidden
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerMint(w, status, `{"kind": "permission_denied", "message": "agent is not assigned to this operator"}`)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	_, err := client.MintAgentToken(context.Background(), sampleAgent, "")
	g.Expect(err).To(MatchError(garam.ErrAgentNotHeld))

	// A refusal that is not a 403, so that the match above is the status and
	// not every refusal reading as one.
	status = http.StatusBadRequest
	_, err = client.MintAgentToken(context.Background(), sampleAgent, "")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err).NotTo(MatchError(garam.ErrAgentNotHeld))
}

func TestClientMintAgentTokenRefusesAnAnswerTheAdapterCouldNotRead(t *testing.T) {
	g := NewWithT(t)
	answer, contract := agentTokenAnswer, "machine-token.v1"
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Garam-Contract-Version", contract)
		answerJSON(w, http.StatusCreated, answer)
	})
	client := garam.NewClient(stub.address(), trustedBy(t, stub))

	// The whole answer, so that each refusal below is the part taken out of it
	// and not the shape of the payload around it.
	_, err := client.MintAgentToken(context.Background(), sampleAgent, "")
	g.Expect(err).NotTo(HaveOccurred())

	for name, broken := range map[string]string{
		"a token not in garam's form": strings.Replace(agentTokenAnswer, sampleToken, sampleToken+" ", 1),
		"another agent's token":       strings.Replace(agentTokenAnswer, string(sampleAgent), "grn:acme:default:agent:other", 1),
		"no epoch":                    strings.Replace(agentTokenAnswer, `"epoch"`, `"notEpoch"`, 1),
		"no expiry":                   strings.Replace(agentTokenAnswer, `"expiresAt"`, `"notExpiresAt"`, 1),
		"no server root":              strings.Replace(agentTokenAnswer, `"serverRootPem"`, `"notServerRootPem"`, 1),
	} {
		answer = broken
		_, err = client.MintAgentToken(context.Background(), sampleAgent, "")
		g.Expect(err).To(HaveOccurred(), "%s was taken", name)
		g.Expect(err.Error()).NotTo(ContainSubstring(sampleToken), "%s put the token in an error", name)
	}

	// An answer under another contract decides nothing.
	answer, contract = agentTokenAnswer, "machine-token.v2"
	_, err = client.MintAgentToken(context.Background(), sampleAgent, "")
	g.Expect(err).To(MatchError(ContainSubstring("under contract")))
}
