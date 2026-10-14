package garam_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

const (
	// controlGRN is the control service's operator, which a fenced agent's
	// control token is minted for.
	controlGRN = "grn:acme:default:operator:control"

	// controlToken is the token the stub mints for the control service.
	controlToken = "garam_at_fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	// lifetime is how long every token the stub mints is accepted for.
	lifetime = time.Hour

	// window is the point in the refresh window the refresher under test picks.
	window = 0.7
)

// memoryTokens is a garam.TokenStore holding what a refresher wrote, and
// refusing a write over a version that moved since it was read, as the API
// server refuses a patch naming a stale resourceVersion.
type memoryTokens struct {
	mu      sync.Mutex
	holders []garam.TokenHolder
	stored  map[garam.GRN]storedTokens
}

// storedTokens is one agent's tokens as written, with when they are due.
type storedTokens struct {
	tokens garam.Tokens
	due    time.Time
}

func (m *memoryTokens) Holders(context.Context) ([]garam.TokenHolder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]garam.TokenHolder(nil), m.holders...), nil
}

func (m *memoryTokens) Store(_ context.Context, holder garam.TokenHolder, tokens garam.Tokens, due time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.holders {
		if m.holders[i].Agent != holder.Agent {
			continue
		}
		if m.holders[i].Version != holder.Version {
			return errors.New("the credential changed since it was read")
		}
		m.holders[i].Due, m.holders[i].Version = due, holder.Version+"'"
	}
	m.stored[holder.Agent] = storedTokens{tokens: tokens, due: due}
	return nil
}

// written is what the refresher wrote, by agent.
func (m *memoryTokens) written() map[garam.GRN]storedTokens {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := map[garam.GRN]storedTokens{}
	for agent, s := range m.stored {
		held[agent] = s
	}
	return held
}

// set runs change on the store's holders under its lock.
func (m *memoryTokens) set(change func(holders []garam.TokenHolder)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m.holders)
}

// mintStub is a garam listener minting tokens at epoch, refusing agents in
// notHeld with 403, and expiring each a lifetime after the test's clock.
type mintStub struct {
	*stubListener
	mu      sync.Mutex
	clock   time.Time
	epoch   int64
	notHeld map[string]bool
}

func newMintStub(t *testing.T, clock time.Time) *mintStub {
	t.Helper()
	m := &mintStub{clock: clock, epoch: 4, notHeld: map[string]bool{}}
	m.stubListener = newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		agent := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/agents/"), "/tokens")
		if m.notHeld[agent] {
			answerMint(w, http.StatusForbidden, `{"kind": "permission_denied", "message": "not assigned at the current epoch"}`)
			return
		}
		var asked struct {
			Audience string `json:"audience"`
		}
		_ = json.NewDecoder(r.Body).Decode(&asked)
		token := sampleToken
		if asked.Audience != "" {
			token = controlToken
		}
		answerMint(w, http.StatusCreated, fmt.Sprintf(`{"token": %q, "grn": %q, "epoch": %d, "audience": %q,
			"expiresAt": %q, "serverRootPem": "a root"}`, token, agent, m.epoch, asked.Audience,
			m.clock.Add(lifetime).Format(time.RFC3339)))
	})
	return m
}

// mints is the audience of every mint the stub answered, by agent, "garam" for
// none.
func (m *mintStub) mints() map[string][]string {
	asked := map[string][]string{}
	for _, request := range m.requests() {
		if !strings.HasSuffix(request.path, "/tokens") {
			continue
		}
		var body struct {
			Audience string `json:"audience"`
		}
		_ = json.Unmarshal([]byte(request.body), &body)
		if body.Audience == "" {
			body.Audience = "garam"
		}
		agent := strings.TrimSuffix(strings.TrimPrefix(request.path, "/agents/"), "/tokens")
		asked[agent] = append(asked[agent], body.Audience)
	}
	return asked
}

// refresherFor is a refresher minting at stub into store, on the test's clock
// and at the fixed point window of the refresh window.
func refresherFor(t *testing.T, stub *mintStub, store *memoryTokens, clock *time.Time) *garam.Refresher {
	t.Helper()
	r := garam.NewRefresher(garam.NewClient(stub.address(), trustedBy(t, stub.stubListener)), store, controlGRN)
	r.Now = func() time.Time { return *clock }
	r.Window = func() float64 { return window }
	return r
}

func TestRefresherMintsWhereNoTokensArePlacedAndIsDueInGaramsWindow(t *testing.T) {
	g := NewWithT(t)
	clock := mintedAt
	stub := newMintStub(t, clock)
	fenced := garam.GRN("grn:acme:default:agent:fenced")
	store := &memoryTokens{stored: map[garam.GRN]storedTokens{}, holders: []garam.TokenHolder{
		{Agent: sampleAgent, Epoch: 4},
		{Agent: fenced, Epoch: 4, Fenced: true},
	}}

	refresherFor(t, stub, store, &clock).Refresh(context.Background())

	// An unfenced agent gets garam's token alone; a fenced one the control
	// service's beside it, minted for the control service's GRN.
	g.Expect(stub.mints()).To(Equal(map[string][]string{
		string(sampleAgent): {"garam"},
		string(fenced):      {"garam", controlGRN},
	}))
	due := mintedAt.Add(time.Duration(float64(lifetime) * window))
	g.Expect(store.written()[sampleAgent]).To(Equal(storedTokens{
		tokens: garam.Tokens{Garam: garam.AgentToken{
			Token: sampleToken, Epoch: 4, ExpiresAt: mintedAt.Add(lifetime), ServerRootPEM: []byte("a root"),
		}},
		due: due,
	}))
	g.Expect(store.written()[fenced].tokens.Control).NotTo(BeNil())
	g.Expect(store.written()[fenced].tokens.Control.Token).To(Equal(controlToken))
	g.Expect(store.written()[fenced].due).To(Equal(due))

	// Every mint was made under this operator's own token, never an agent's:
	// no token is exchanged for its successor.
	for _, request := range stub.requests() {
		g.Expect(request.token).NotTo(Or(Equal(sampleToken), Equal(controlToken)))
	}
}

func TestRefresherMintsAgainOnlyOnceDueBeforeTheTokenExpires(t *testing.T) {
	g := NewWithT(t)
	clock := mintedAt
	stub := newMintStub(t, clock)
	store := &memoryTokens{stored: map[garam.GRN]storedTokens{}, holders: []garam.TokenHolder{{Agent: sampleAgent}}}
	refresher := refresherFor(t, stub, store, &clock)
	refresher.Refresh(context.Background())
	due := store.written()[sampleAgent].due
	g.Expect(due).To(BeTemporally("<", mintedAt.Add(lifetime)), "the refresh is not before the token expires")

	// Before it is due, a pass mints nothing.
	clock = due.Add(-time.Second)
	refresher.Refresh(context.Background())
	g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(1))

	// Control: once it is due, the next pass mints again.
	clock = due
	refresher.Refresh(context.Background())
	g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(2))
}

func TestRefresherWindowIsGaramsTwoThirdsToThreeQuarters(t *testing.T) {
	g := NewWithT(t)
	refresher := garam.NewRefresher(nil, &memoryTokens{}, "")
	for range 1000 {
		g.Expect(refresher.Window()).To(And(BeNumerically(">=", 2.0/3.0), BeNumerically("<=", 3.0/4.0)))
	}
}

func TestRefresherMintsAgainAtOnceAfterARecovery(t *testing.T) {
	g := NewWithT(t)
	clock := mintedAt
	stub := newMintStub(t, clock)
	store := &memoryTokens{stored: map[garam.GRN]storedTokens{}, holders: []garam.TokenHolder{{Agent: sampleAgent, Epoch: 4}}}
	refresher := refresherFor(t, stub, store, &clock)
	refresher.Refresh(context.Background())
	g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(1))

	// The recoverer records the new lineage and removes the refresh time, as
	// credential.Secrets does once a recovery is finalized.
	clock = mintedAt.Add(time.Minute)
	store.set(func(holders []garam.TokenHolder) { holders[0].Due, holders[0].Version = time.Time{}, "recovered" })
	refresher.Refresh(context.Background())
	g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(2), "the agent was not minted for in the new lineage")
	// Due in the window of the new token, which the stub minted to expire at
	// the same moment as the first.
	g.Expect(store.written()[sampleAgent].due).To(Equal(clock.Add(time.Duration(float64(mintedAt.Add(lifetime).Sub(clock)) * window))))
}

func TestRefresherReportsAMintRefusedForAStaleEpochAndDoesNotRetryItAsARefresh(t *testing.T) {
	for name, refuse := range map[string]func(stub *mintStub, store *memoryTokens){
		"garam refusing the mint": func(stub *mintStub, _ *memoryTokens) {
			stub.mu.Lock()
			defer stub.mu.Unlock()
			stub.notHeld[string(sampleAgent)] = true
		},
		"garam minting at another epoch than the spec's": func(_ *mintStub, store *memoryTokens) {
			store.set(func(holders []garam.TokenHolder) { holders[0].Epoch = 3 })
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			clock := mintedAt
			stub := newMintStub(t, clock)
			other := garam.GRN("grn:acme:default:agent:other")
			store := &memoryTokens{stored: map[garam.GRN]storedTokens{}, holders: []garam.TokenHolder{
				{Agent: sampleAgent, Epoch: 4}, {Agent: other, Epoch: 4},
			}}
			refuse(stub, store)
			refresher := refresherFor(t, stub, store, &clock)

			refresher.Refresh(context.Background())
			g.Expect(store.written()).NotTo(HaveKey(sampleAgent), "a refused token was written")
			// Control: the other agent, which garam mints for at its spec's
			// epoch, is written.
			g.Expect(store.written()).To(HaveKey(other))

			// Not asked again on the next passes, as a refresh would be.
			clock = mintedAt.Add(time.Minute)
			refresher.Refresh(context.Background())
			g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(1))

			// Asked again once the refusal's wait has passed.
			clock = mintedAt.Add(6 * time.Minute)
			refresher.Refresh(context.Background())
			g.Expect(stub.mints()[string(sampleAgent)]).To(HaveLen(2))
		})
	}
}
