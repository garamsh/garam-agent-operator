package machinetoken

import (
	"errors"
	"fmt"
	"time"
)

// Files names the three files a deployer places from one key issuance (garam@9ba35c3
// api/openapi.yaml IssuedOperatorKey): the private key, the operator GRN garam answered as
// clientId, and the tokenUrl every assertion names as its audience. All three are read at each
// exchange, so a key replaced in place is the one the next exchange signs with.
type Files struct {
	PrivateKey string
	ClientID   string
	TokenURL   string
}

// Reason is why an exchange failed, as the failure is counted and alerted on.
type Reason string

const (
	// ReasonKey is an identity whose files could not be read or parsed. Nothing was sent.
	ReasonKey Reason = "key"
	// ReasonInvalidClient is garam's invalid_client: the assertion authenticated no operator.
	ReasonInvalidClient Reason = "invalid_client"
	// ReasonThrottled is garam's 429.
	ReasonThrottled Reason = "throttled"
	// ReasonRefused is any other refusal garam decided.
	ReasonRefused Reason = "refused"
	// ReasonUnavailable is garam unreachable, or answering 5xx or an answer that is not a token.
	ReasonUnavailable Reason = "unavailable"
)

// ErrInvalidClient is garam refusing the assertion as invalid_client: a bad signature, a key that
// is unknown, withdrawn or past its rollover, a wrong identity, audience or time, or a replay are
// one refusal (garam@9ba35c3 api/machine.yaml exchangeClientAssertion). No other credential is
// tried in its place.
var ErrInvalidClient = errors.New("garam refused this operator's client assertion (invalid_client)")

// Error is a failed exchange: why, and when the next one is sent.
type Error struct {
	Reason Reason
	// Until is when the source sends its next exchange; a token asked for before then answers
	// this error again without contacting garam.
	Until time.Time
	Err   error
}

func (e *Error) Error() string {
	return fmt.Sprintf("exchange a client assertion for a garam token (%s, next attempt at %s): %v",
		e.Reason, e.Until.UTC().Format(time.RFC3339), e.Err)
}

func (e *Error) Unwrap() error { return e.Err }
