// Package garamfake is a stand-in for garam's machine listener, for the
// operator's tests. It serves the three operations the operator calls,
// with the answers platform/api/machine.yaml gives them:
// exchangeClientAssertion, mintAgentToken and reportProvisioningState.
//
// The exchange enforces what ADR-0151 §6 says garam enforces of an
// assertion: RS256, `typ` JWT, `kid` the key's RFC 7638 thumbprint,
// `iss` and `sub` the client id, `aud` the token URL, integer `iat` and
// `exp` at most 60 seconds apart, and a `jti` it has not seen. The
// other two operations accept only a token the exchange issued.
//
// It does not prove garam: it holds no organization, no assignment and
// no epoch beyond what a test sets.
package garamfake

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exchange is one accepted exchange.
type Exchange struct {
	// Audience is the form's `audience`, empty where absent.
	Audience string
	Token    string
}

// Mint is one mintAgentToken request.
type Mint struct {
	Agent    string
	Audience string
	Contract string
	Bearer   string
}

// Report is one reportProvisioningState request.
type Report struct {
	Agent  string
	Epoch  int
	State  string
	Bearer string
}

// Server is the fake. Its fields are read and written through methods,
// which are safe for concurrent use.
type Server struct {
	clientID string
	key      *rsa.PublicKey
	srv      *httptest.Server

	mu         sync.Mutex
	seenJTI    map[string]bool
	issued     map[string]bool
	exchanges  []Exchange
	mints      []Mint
	reports    []Report
	expiresIn  int
	epoch      int
	reject401  int
	refuseWith *Refusal
	exchangeAs *int
}

// Refusal is the answer a test makes reportProvisioningState give.
type Refusal struct {
	Status  int
	Kind    string
	Message string
}

// New starts the fake for the operator clientID that holds key.
func New(t *testing.T, clientID string, key *rsa.PublicKey) *Server {
	t.Helper()
	s := &Server{
		clientID: clientID, key: key, seenJTI: map[string]bool{}, issued: map[string]bool{},
		expiresIn: 3600, epoch: 1,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", s.exchange)
	mux.HandleFunc("POST /agents/{agent}/tokens", s.mint)
	mux.HandleFunc("POST /agents/{agent}/provisioning-state", s.report)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// WriteKey generates an RSA key, writes it as a PKCS#8 PEM file with
// mode 0600 and returns the file's path and the key.
func WriteKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "operator.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

// URL is the machine listener's base URL.
func (s *Server) URL() string { return s.srv.URL }

// Client is an HTTP client for the listener.
func (s *Server) Client() *http.Client { return s.srv.Client() }

// TokenURL is the token URL a key issuance would answer.
func (s *Server) TokenURL() string { return s.srv.URL + "/oauth/token" }

// SetExpiresIn sets the lifetime, in seconds, of the tokens issued from
// now on.
func (s *Server) SetExpiresIn(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiresIn = seconds
}

// SetEpoch sets the epoch mintAgentToken answers.
func (s *Server) SetEpoch(epoch int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch = epoch
}

// Reject401 makes the next n requests to mintAgentToken and
// reportProvisioningState answer 401, as a token garam stopped
// accepting is answered.
func (s *Server) Reject401(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject401 = n
}

// RefuseReports makes reportProvisioningState answer with r.
func (s *Server) RefuseReports(r Refusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseWith = &r
}

// ExchangeAnswers makes the exchange answer status with an OAuth error,
// until ExchangeAnswers(0).
func (s *Server) ExchangeAnswers(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == 0 {
		s.exchangeAs = nil
		return
	}
	s.exchangeAs = &status
}

// Exchanges returns the exchanges accepted so far.
func (s *Server) Exchanges() []Exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Exchange(nil), s.exchanges...)
}

// Mints returns the mintAgentToken requests served so far.
func (s *Server) Mints() []Mint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mint(nil), s.mints...)
}

// Reports returns the reportProvisioningState requests served so far.
func (s *Server) Reports() []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Report(nil), s.reports...)
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exchangeAs != nil {
		if *s.exchangeAs == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		oauthError(w, *s.exchangeAs, "temporarily_unavailable")
		return
	}
	if r.Header.Get("Authorization") != "" ||
		r.PostForm.Get("grant_type") != "client_credentials" ||
		r.PostForm.Get("client_id") != s.clientID ||
		r.PostForm.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !s.assertionHolds(r.PostForm.Get("client_assertion"), "http://"+r.Host+r.URL.Path) {
		oauthError(w, http.StatusBadRequest, "invalid_client")
		return
	}
	token := "garam_ot_" + strconv.Itoa(len(s.issued)+1) + strings.Repeat("0", 8)
	s.issued[token] = true
	s.exchanges = append(s.exchanges, Exchange{Audience: r.PostForm.Get("audience"), Token: token})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "expires_in": s.expiresIn, "token_type": "Bearer",
	})
}

// assertionHolds checks the assertion as garam does. The caller holds
// the lock.
func (s *Server) assertionHolds(jwt, tokenURL string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	var header struct{ Alg, Kid, Typ string }
	var claims struct {
		Iss, Sub, Aud, Jti string
		Iat, Exp           json.Number
	}
	if !decodePart(parts[0], &header) || !decodePart(parts[1], &claims) {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(s.key, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	iat, err1 := claims.Iat.Int64()
	exp, err2 := claims.Exp.Int64()
	jti, err3 := base64.RawURLEncoding.DecodeString(claims.Jti)
	ok := header.Alg == "RS256" && header.Typ == "JWT" && header.Kid == thumbprint(s.key) &&
		claims.Iss == s.clientID && claims.Sub == s.clientID && claims.Aud == tokenURL &&
		err1 == nil && err2 == nil && err3 == nil && len(claims.Jti) >= 22 && len(claims.Jti) <= 128 &&
		len(jti) > 0 && exp > iat && exp-iat <= 60 && !s.seenJTI[claims.Jti]
	if ok {
		s.seenJTI[claims.Jti] = true
	}
	return ok
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.reject401 > 0 || !found || !s.issued[bearer] {
		if s.reject401 > 0 {
			s.reject401--
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"kind": "unauthenticated", "message": "token refused"})
		return "", false
	}
	return bearer, true
}

func (s *Server) mint(w http.ResponseWriter, r *http.Request) {
	bearer, ok := s.authorized(w, r)
	if !ok {
		return
	}
	var body struct {
		Audience *string `json:"audience"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"kind": "invalid_argument", "message": "body"})
		return
	}
	m := Mint{Agent: r.PathValue("agent"), Contract: r.Header.Get("Garam-Contract-Version"), Bearer: bearer}
	if body.Audience != nil {
		m.Audience = *body.Audience
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mints = append(s.mints, m)
	prefix := "garam_at_"
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": prefix + strconv.Itoa(len(s.mints)) + strings.Repeat("a", 8), "epoch": s.epoch,
		"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"grn":       m.Agent, "serverRootPem": "root",
	})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	bearer, ok := s.authorized(w, r)
	if !ok {
		return
	}
	var body struct {
		Epoch int    `json:"epoch"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"kind": "invalid_argument", "message": "body"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports,
		Report{Agent: r.PathValue("agent"), Epoch: body.Epoch, State: body.State, Bearer: bearer})
	if s.refuseWith != nil {
		writeJSON(w, s.refuseWith.Status, map[string]string{"kind": s.refuseWith.Kind, "message": s.refuseWith.Message})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodePart(part string, into any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	return dec.Decode(into) == nil
}

// thumbprint is the RFC 7638 SHA-256 thumbprint of key, which garam
// knows the key by.
func thumbprint(key *rsa.PublicKey) string {
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
