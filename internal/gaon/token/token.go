// Package token holds the operator's bearer tokens: it signs the
// private_key_jwt assertion with the key garam issued, exchanges it at
// garam's token endpoint and keeps the answer in memory until shortly
// before it lapses (ADR-0151 §6).
package token

import (
	"context"
	"crypto/rsa"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/gaon/keyfile"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/platform-sdk-go/machine"
)

// DefaultMargin is how long before expiry a token is exchanged again.
const DefaultMargin = time.Minute

// Options are what one Source is built from.
type Options struct {
	// MachineURL is the base URL of garam's machine listener.
	MachineURL string
	// HTTPClient reaches it. It trusts garam's root.
	HTTPClient *http.Client
	// ClientID is the operator's GRN.
	ClientID string
	// TokenURL is the token URL the key issuance answered. It is the
	// assertion's `aud`, byte for byte.
	TokenURL string
	// KeyFile is the operator's private key, read at every exchange.
	KeyFile string
	// Audience is the GRN of the operator the token is for. Empty, the
	// token is garam's own.
	Audience string
	// Margin defaults to DefaultMargin.
	Margin time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// Source returns a live bearer token.
type Source struct {
	opts   Options
	client *machine.ClientWithResponses

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// New returns the Source over o. It reads the key once, so a key the
// rule refuses or that is not one stops the operator at start; the key
// is read again at every exchange.
func New(o Options) (*Source, error) {
	if _, err := loadKey(o.KeyFile); err != nil {
		return nil, err
	}
	if o.Margin == 0 {
		o.Margin = DefaultMargin
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	client, err := machine.NewClientWithResponses(o.MachineURL, machine.WithHTTPClient(o.HTTPClient))
	if err != nil {
		return nil, errorx.Wrap(err, "build the machine client for the exchange", errorx.KindInvalidArgument)
	}
	return &Source{opts: o, client: client}, nil
}

// Token returns the held token, or exchanges the key again where it has
// lapsed or lapses within the margin.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	if s.token != "" && now.Add(s.opts.Margin).Before(s.expiry) {
		return s.token, nil
	}
	token, expiry, err := s.exchange(ctx, now)
	if err != nil {
		return "", err
	}
	s.token, s.expiry = token, expiry
	return token, nil
}

// Invalidate drops the held token, for a caller a peer refused it to.
func (s *Source) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token, s.expiry = "", time.Time{}
}

func (s *Source) exchange(ctx context.Context, now time.Time) (string, time.Time, error) {
	key, err := loadKey(s.opts.KeyFile)
	if err != nil {
		return "", time.Time{}, err
	}
	signed, err := assertion(key, s.opts.ClientID, s.opts.TokenURL, now)
	if err != nil {
		return "", time.Time{}, err
	}
	req := machine.ClientAssertionRequest{
		GrantType:           machine.ClientCredentials,
		ClientId:            s.opts.ClientID,
		ClientAssertionType: machine.ClientAssertionRequestClientAssertionTypeUrnIetfParamsOauthClientAssertionTypeJwtBearer,
		ClientAssertion:     signed,
	}
	if s.opts.Audience != "" {
		req.Audience = &s.opts.Audience
	}
	res, err := s.client.ExchangeClientAssertionWithFormdataBodyWithResponse(ctx, req)
	if err != nil {
		return "", time.Time{}, errorx.Wrap(err, "exchange the client assertion", errorx.KindUnavailable)
	}
	return answer(res, now)
}

// loadKey reads the key file under the key-file rule and parses it.
func loadKey(path string) (*rsa.PrivateKey, error) {
	raw, err := keyfile.Read(path)
	if err != nil {
		return nil, err
	}
	return parseKey(raw)
}

// answer reads the token endpoint's answer: a token only from a 200
// that carries a bearer token with a lifetime, and a typed error
// otherwise.
func answer(res *machine.ExchangeClientAssertionResponse, now time.Time) (string, time.Time, error) {
	switch status := res.StatusCode(); {
	case status == http.StatusOK:
		t := res.JSON200
		if t == nil || t.AccessToken == "" || t.ExpiresIn <= 0 || !strings.EqualFold(string(t.TokenType), "bearer") {
			return "", time.Time{}, errorx.New(errorx.KindInternal, "token endpoint answered 200 without a bearer token and a lifetime")
		}
		return t.AccessToken, now.Add(time.Duration(t.ExpiresIn) * time.Second), nil
	case status == http.StatusTooManyRequests:
		return "", time.Time{}, errorx.New(errorx.KindResourceExhausted,
			"token endpoint refused the exchange as too frequent"+retryAfter(res))
	case status == http.StatusBadRequest:
		return "", time.Time{}, errorx.New(errorx.KindUnauthenticated, "token endpoint refused the assertion: "+oauthCode(res))
	case status >= http.StatusInternalServerError:
		return "", time.Time{}, errorx.New(errorx.KindUnavailable, "token endpoint answered "+strconv.Itoa(status))
	default:
		return "", time.Time{}, errorx.New(errorx.KindInternal, "token endpoint answered "+strconv.Itoa(status))
	}
}

func retryAfter(res *machine.ExchangeClientAssertionResponse) string {
	if res.Headers429 == nil || res.Headers429.RetryAfter == nil {
		return ""
	}
	return ", retry after " + strconv.Itoa(*res.Headers429.RetryAfter) + "s"
}

func oauthCode(res *machine.ExchangeClientAssertionResponse) string {
	if res.JSON400 == nil {
		return "unreadable error"
	}
	return string(res.JSON400.Error)
}
