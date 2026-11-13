package token_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/token"
	"github.com/garamsh/garam-agent-operator/tests/gaon/garamfake"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

var clientID = grn.New("org1", "operator", "op1").String()

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	fake  *garamfake.Server
	clock *clock
	opts  token.Options
}

func newRig(t *testing.T) rig {
	t.Helper()
	path, key := garamfake.WriteKey(t)
	fake := garamfake.New(t, clientID, &key.PublicKey)
	clk := &clock{t: time.Now()}
	return rig{fake: fake, clock: clk, opts: token.Options{
		MachineURL: fake.URL(), HTTPClient: fake.Client(), ClientID: clientID,
		TokenURL: fake.TokenURL(), KeyFile: path, Now: clk.now,
	}}
}

func (r rig) source(t *testing.T) *token.Source {
	t.Helper()
	s, err := token.New(r.opts)
	require.NoError(t, err)
	return s
}

func TestExchangeIsAcceptedAsGaramAcceptsAnAssertion(t *testing.T) {
	r := newRig(t)
	got, err := r.source(t).Token(context.Background())
	require.NoError(t, err)
	// The fake verifies the signature, typ, kid, iss, sub, aud, the
	// 60-second window and the jti, so an accepted exchange proves them.
	ex := r.fake.Exchanges()
	require.Len(t, ex, 1)
	assert.Equal(t, ex[0].Token, got)
	assert.Empty(t, ex[0].Audience)
}

func TestAssertionForAnotherTokenURLIsRefused(t *testing.T) {
	r := newRig(t)
	r.opts.TokenURL = r.fake.URL() + "/elsewhere"
	_, err := r.source(t).Token(context.Background())
	require.Error(t, err)
	assert.Equal(t, errorx.KindUnauthenticated, errorx.KindOf(err))
	assert.Empty(t, r.fake.Exchanges())
}

func TestAudienceIsSentWhenSet(t *testing.T) {
	r := newRig(t)
	r.opts.Audience = grn.New("org1", "operator", "ctl1").String()
	_, err := r.source(t).Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, r.opts.Audience, r.fake.Exchanges()[0].Audience)
}

func TestTokenIsHeldThenExchangedAgainBeforeExpiry(t *testing.T) {
	r := newRig(t)
	r.fake.SetExpiresIn(600)
	s := r.source(t)
	first, err := s.Token(context.Background())
	require.NoError(t, err)

	r.clock.advance(8 * time.Minute) // 2 minutes left, outside the margin
	held, err := s.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, first, held)
	assert.Len(t, r.fake.Exchanges(), 1)

	r.clock.advance(time.Minute + time.Second) // inside the 1 minute margin
	next, err := s.Token(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, first, next)
	assert.Len(t, r.fake.Exchanges(), 2)
}

func TestInvalidateForcesAnExchange(t *testing.T) {
	r := newRig(t)
	s := r.source(t)
	first, err := s.Token(context.Background())
	require.NoError(t, err)
	s.Invalidate()
	next, err := s.Token(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, first, next)
}

func TestKeyIsReadAtEveryExchange(t *testing.T) {
	r := newRig(t)
	r.fake.SetExpiresIn(600)
	s := r.source(t)
	_, err := s.Token(context.Background())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(r.opts.KeyFile, []byte("not a key"), 0o600))
	r.clock.advance(time.Hour)
	_, err = s.Token(context.Background())
	require.Error(t, err)
	assert.Len(t, r.fake.Exchanges(), 1)
}

func TestKeyFileReadableByOthersStopsTheStart(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.Chmod(r.opts.KeyFile, 0o640))
	_, err := token.New(r.opts)
	require.Error(t, err)
	assert.Equal(t, errorx.KindInvalidArgument, errorx.KindOf(err))
}

func TestKeyThatIsNotAKeyStopsTheStart(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.WriteFile(r.opts.KeyFile, []byte("nope"), 0o600))
	_, err := token.New(r.opts)
	require.Error(t, err)
}

func TestRefusalsAreMapped(t *testing.T) {
	cases := []struct {
		status int
		kind   errorx.Kind
	}{
		{400, errorx.KindUnauthenticated},
		{429, errorx.KindResourceExhausted},
		{500, errorx.KindUnavailable},
		{503, errorx.KindUnavailable},
	}
	for _, c := range cases {
		t.Run(errorx.KindOf(errorx.New(c.kind, "x")).String(), func(t *testing.T) {
			r := newRig(t)
			r.fake.ExchangeAnswers(c.status)
			_, err := r.source(t).Token(context.Background())
			require.Error(t, err)
			assert.Equal(t, c.kind, errorx.KindOf(err))
			if c.status == 429 {
				assert.Contains(t, err.Error(), "retry after 7s")
			}

			// Control: the same source works once the endpoint does.
			r.fake.ExchangeAnswers(0)
			_, err = r.source(t).Token(context.Background())
			require.NoError(t, err)
		})
	}
}
