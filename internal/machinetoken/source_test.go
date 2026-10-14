package machinetoken_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

const (
	// unavailable is garam's answer to an exchange it cannot decide.
	unavailable = `{"error":"temporarily_unavailable"}`

	clientID = "grn:acme:default:operator:controller"
	// tokenURL is garam's configured value, deliberately not the address the stub listens on.
	tokenURL = "https://machine.garam.example/oauth/token"
)

// exchange is one request the stub's token endpoint received.
type exchange struct {
	form          map[string][]string
	authorization string
	header        map[string]any
	claims        map[string]any
	verifiedWith  *rsa.PublicKey
}

// stub is garam's machine listener: /oauth/token answers what answer says, and /protected
// accepts only the tokens in accepted.
type stub struct {
	t      *testing.T
	server *httptest.Server
	keys   []*rsa.PublicKey

	mu        sync.Mutex
	exchanges []exchange
	answer    func(n int) (int, http.Header, string)
	accepted  map[string]bool
	presented []string
}

func newStub(t *testing.T, keys ...*rsa.PublicKey) *stub {
	t.Helper()
	s := &stub{t: t, keys: keys, accepted: map[string]bool{}}
	s.answer = func(n int) (int, http.Header, string) {
		return http.StatusOK, nil, fmt.Sprintf(`{"access_token":"garam_ot_%064d","token_type":"Bearer","expires_in":3600}`, n)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("GET /protected", s.protected)
	s.server = httptest.NewTLSServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) token(w http.ResponseWriter, r *http.Request) {
	require.NoError(s.t, r.ParseForm())
	got := exchange{form: r.PostForm, authorization: r.Header.Get("Authorization")}
	raw := r.PostForm.Get("client_assertion")
	if parts := strings.Split(raw, "."); len(parts) == 3 {
		decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
		require.NoError(s.t, err)
		require.NoError(s.t, json.Unmarshal(decoded, &got.header))
		parsed, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
		require.NoError(s.t, err)
		for _, key := range s.keys {
			if err := parsed.Claims(key, &got.claims); err == nil {
				got.verifiedWith = key
				break
			}
		}
	}
	s.mu.Lock()
	s.exchanges = append(s.exchanges, got)
	status, header, body := s.answer(len(s.exchanges))
	s.mu.Unlock()
	maps.Copy(w.Header(), header)
	w.Header().Set("Garam-Contract-Version", "machine-token.v2")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *stub) protected(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.presented = append(s.presented, token)
	ok := s.accepted[token]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *stub) sent() []exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]exchange(nil), s.exchanges...)
}

// identity writes a key issuance's three files and answers them, and the key's public half.
func identity(t *testing.T, dir string) (machinetoken.Files, *rsa.PublicKey) {
	t.Helper()
	files := machinetoken.Files{
		PrivateKey: filepath.Join(dir, "private-key.pem"),
		ClientID:   filepath.Join(dir, "client-id"),
		TokenURL:   filepath.Join(dir, "token-url"),
	}
	require.NoError(t, os.WriteFile(files.ClientID, []byte(clientID+"\n"), 0o600))
	require.NoError(t, os.WriteFile(files.TokenURL, []byte(tokenURL), 0o600))
	return files, writeKey(t, files.PrivateKey)
}

// writeKey places a fresh PKCS#8 RSA key at path, as garam's key issuance answers one.
func writeKey(t *testing.T, path string) *rsa.PublicKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	return &key.PublicKey
}

func kidOf(t *testing.T, key *rsa.PublicKey) string {
	t.Helper()
	sum, err := (&jose.JSONWebKey{Key: key}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(sum)
}

// clock is a settable time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func newSource(s *stub, files machinetoken.Files, c *clock, audience string) *machinetoken.Source {
	return machinetoken.NewSource(machinetoken.Config{
		Endpoint: s.server.URL + "/oauth/token",
		Files:    files,
		Audience: audience,
		HTTP:     s.server.Client(),
		Now:      c.Now,
	})
}

func TestSource_SignsTheAssertionTheContractNames(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	c := &clock{now: time.Unix(1_800_000_000, 0)}

	token, err := newSource(s, files, c, "").Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("garam_ot_%064d", 1), token)

	sent := s.sent()
	require.Len(t, sent, 1)
	got := sent[0]
	assert.Empty(t, got.authorization, "the exchange presents no Authorization header")
	assert.Equal(t, []string{"client_credentials"}, got.form["grant_type"])
	assert.Equal(t, []string{clientID}, got.form["client_id"])
	assert.Equal(t, []string{"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, got.form["client_assertion_type"])
	assert.NotContains(t, got.form, "audience")

	assert.Equal(t, map[string]any{"alg": "RS256", "kid": kidOf(t, public), "typ": "JWT"}, got.header,
		"the protected header carries alg, kid and typ, and nothing else")
	require.Same(t, public, got.verifiedWith, "the assertion verifies under the identity's key")
	assert.Equal(t, clientID, got.claims["iss"])
	assert.Equal(t, clientID, got.claims["sub"])
	assert.Equal(t, tokenURL, got.claims["aud"], "aud is one string, byte-equal to tokenUrl")
	iat, exp := got.claims["iat"].(float64), got.claims["exp"].(float64)
	assert.Equal(t, float64(1_800_000_000), iat)
	assert.LessOrEqual(t, exp-iat, float64(60))
	assert.Greater(t, exp, iat)
	jti, err := base64.RawURLEncoding.DecodeString(got.claims["jti"].(string))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(jti)*8, 128, "jti carries at least 128 random bits")
}

func TestSource_NamesTheAudienceItIsAskedFor(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	c := &clock{now: time.Unix(1_800_000_000, 0)}

	_, err := newSource(s, files, c, "grn:acme:default:operator:control").Token(context.Background())
	require.NoError(t, err)

	require.Len(t, s.sent(), 1)
	assert.Equal(t, []string{"grn:acme:default:operator:control"}, s.sent()[0].form["audience"])
}

func TestSource_SendsAFreshJTIOnEveryAttempt(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = func(n int) (int, http.Header, string) {
		if n == 1 {
			return http.StatusServiceUnavailable, nil, unavailable
		}
		return http.StatusOK, nil, fmt.Sprintf(`{"access_token":"garam_ot_%064d","token_type":"Bearer","expires_in":3600}`, n)
	}
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")

	_, err := source.Token(context.Background())
	require.Error(t, err, "the first attempt is refused")
	c.Add(time.Minute)
	first, err := source.Token(context.Background())
	require.NoError(t, err)
	source.Invalidate(first)
	_, err = source.Token(context.Background())
	require.NoError(t, err)

	sent := s.sent()
	require.Len(t, sent, 3)
	seen := map[string]bool{}
	for _, e := range sent {
		jti := e.claims["jti"].(string)
		assert.False(t, seen[jti], "jti %q was sent twice", jti)
		seen[jti] = true
	}
}

func TestSource_HonoursRetryAfterOnA429(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = func(n int) (int, http.Header, string) {
		if n == 1 {
			return http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, unavailable
		}
		return http.StatusOK, nil, `{"access_token":"garam_ot_` + strings.Repeat("a", 64) + `","token_type":"Bearer","expires_in":3600}`
	}
	start := time.Unix(1_800_000_000, 0)
	c := &clock{now: start}
	source := newSource(s, files, c, "")

	_, err := source.Token(context.Background())
	var failed *machinetoken.Error
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, machinetoken.ReasonThrottled, failed.Reason)
	assert.Equal(t, start.Add(30*time.Second), failed.Until)

	c.Add(29 * time.Second)
	_, err = source.Token(context.Background())
	require.ErrorAs(t, err, &failed)
	assert.Len(t, s.sent(), 1, "nothing is sent before Retry-After has passed")

	c.Add(time.Second)
	_, err = source.Token(context.Background())
	require.NoError(t, err)
	assert.Len(t, s.sent(), 2, "the exchange is sent again once Retry-After has passed")
}

func TestSource_ReportsInvalidClientByNameAndTriesNothingElse(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusBadRequest, nil, `{"error":"invalid_client"}`
	}
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	var told []*machinetoken.Error
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: s.server.URL + "/oauth/token", Files: files, HTTP: s.server.Client(), Now: c.Now,
		Failed: func(e *machinetoken.Error) { told = append(told, e) },
	})
	transport := &machinetoken.Transport{Source: source, Base: s.server.Client().Transport}

	request, err := http.NewRequest(http.MethodGet, s.server.URL+"/protected", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(request)

	require.ErrorIs(t, err, machinetoken.ErrInvalidClient)
	assert.Contains(t, err.Error(), "invalid_client")
	require.Len(t, told, 1)
	assert.Equal(t, machinetoken.ReasonInvalidClient, told[0].Reason)
	assert.Len(t, s.sent(), 1)
	assert.Empty(t, s.presented, "no protected call is sent without a token")

	// Control: an exchange refused for another reason is not reported as invalid_client.
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusBadRequest, nil, `{"error":"invalid_request"}`
	}
	c.Add(time.Hour)
	_, err = source.Token(context.Background())
	require.ErrorAs(t, err, new(*machinetoken.Error))
	assert.NotErrorIs(t, err, machinetoken.ErrInvalidClient)
}

func TestTransport_ReExchangesOnceAfterA401(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	second := fmt.Sprintf("garam_ot_%064d", 2)
	s.accepted[second] = true
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	transport := &machinetoken.Transport{Source: newSource(s, files, c, ""), Base: s.server.Client().Transport}

	request, err := http.NewRequest(http.MethodGet, s.server.URL+"/protected", nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	_ = response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, []string{fmt.Sprintf("garam_ot_%064d", 1), second}, s.presented)
	require.Len(t, s.sent(), 2)
	assert.NotEqual(t, s.sent()[0].claims["jti"], s.sent()[1].claims["jti"])
	assert.Empty(t, request.Header.Get("Authorization"), "the caller's request is not changed")

	// A 401 under the fresh token too is answered as it came: one re-exchange, not a loop.
	s.accepted[second] = false
	c.Add(time.Hour)
	response, err = transport.RoundTrip(request)
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
	assert.Len(t, s.sent(), 4)
	assert.Len(t, s.presented, 4)
}

func TestSource_SignsWithTheKeyFileAsItIsAtEachExchange(t *testing.T) {
	dir := t.TempDir()
	files, first := identity(t, dir)
	s := newStub(t, first)
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")

	token, err := source.Token(context.Background())
	require.NoError(t, err)
	replaced := writeKey(t, files.PrivateKey)
	s.keys = append(s.keys, replaced)
	source.Invalidate(token)
	_, err = source.Token(context.Background())
	require.NoError(t, err)

	sent := s.sent()
	require.Len(t, sent, 2)
	assert.Same(t, first, sent[0].verifiedWith)
	assert.Equal(t, kidOf(t, first), sent[0].header["kid"])
	assert.Same(t, replaced, sent[1].verifiedWith, "the replaced key signs the next exchange")
	assert.Equal(t, kidOf(t, replaced), sent[1].header["kid"], "kid names the key that signed")
}

func TestSource_RefreshesBeforeTheTokenExpires(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")

	_, err := source.Token(context.Background())
	require.NoError(t, err)
	c.Add(39 * time.Minute)
	_, err = source.Token(context.Background())
	require.NoError(t, err)
	assert.Len(t, s.sent(), 1, "a token in the first two thirds of its hour is presented as held")

	c.Add(2 * time.Minute)
	token, err := source.Token(context.Background())
	require.NoError(t, err)
	assert.Len(t, s.sent(), 2, "a token in its last third is replaced before it expires")
	assert.Equal(t, fmt.Sprintf("garam_ot_%064d", 2), token)
}

func TestSource_BacksOffWithABoundAndKeepsALiveToken(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	var waits []time.Duration
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: s.server.URL + "/oauth/token", Files: files, HTTP: s.server.Client(), Now: c.Now,
		Failed: func(e *machinetoken.Error) { waits = append(waits, e.Until.Sub(c.Now())) },
	})
	held, err := source.Token(context.Background())
	require.NoError(t, err)
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusServiceUnavailable, nil, unavailable
	}

	c.Add(41 * time.Minute)
	token, err := source.Token(context.Background())
	require.NoError(t, err, "a failed refresh answers the token still held while it lives")
	assert.Equal(t, held, token)
	c.Add(time.Second / 2)
	_, _ = source.Token(context.Background())
	assert.Len(t, s.sent(), 2, "nothing is sent inside the backoff")

	for range 12 {
		c.Add(waits[len(waits)-1])
		_, err = source.Token(context.Background())
	}
	require.Len(t, waits, 13)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, waits[:4],
		"the wait doubles")
	assert.Equal(t, 5*time.Minute, waits[len(waits)-1], "the wait is bounded")

	var failed *machinetoken.Error
	require.ErrorAs(t, err, &failed, "an expired token is not presented")
	assert.Equal(t, machinetoken.ReasonUnavailable, failed.Reason)
	assert.NotErrorIs(t, err, machinetoken.ErrInvalidClient)
}
