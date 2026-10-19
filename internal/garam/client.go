package garam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// requestTimeout bounds one call to garam. It is what stops a listener that
// accepted the connection and then said nothing from holding a refresh open
// until the manager stops.
const requestTimeout = 30 * time.Second

// Client mints the tokens of the agents this operator holds at garam's machine
// listener. What the caller is scopes it: garam mints only for an agent the
// token's operator holds.
type Client struct {
	address string
	http    *http.Client
}

// NewClient returns a Client reaching garam's machine listener at address, a
// host and port, through transport, which presents this operator's token.
func NewClient(address string, transport http.RoundTripper) *Client {
	return &Client{
		address: address,
		http:    &http.Client{Timeout: requestTimeout, Transport: transport},
	}
}

// MintAgentToken asks garam for a token of an agent this operator holds at the
// current epoch, under machine-token.v1 (garam@afde8b8 api/machine.yaml
// mintAgentToken). audience names the hosted operator the token is minted for,
// and is empty for a token garam itself accepts. garam mints it in the lineage
// the agent's execution holds, and a mint voids no earlier token.
//
// An agent this operator does not hold at the current epoch answers
// [ErrAgentNotHeld]. The token is answered once: nothing here logs it or puts
// it in an error.
func (c *Client) MintAgentToken(ctx context.Context, agent GRN, audience string) (AgentToken, error) {
	var answered agentTokenPayload
	err := c.send(ctx, http.MethodPost, "/agents/"+url.PathEscape(string(agent))+"/tokens", tokenContract,
		tokenRequest{Audience: audience}, &answered)

	var refused *refusal
	if errors.As(err, &refused) && refused.status == http.StatusForbidden {
		return AgentToken{}, fmt.Errorf("mint a token for %s: %w: %w", agent, ErrAgentNotHeld, refused)
	}
	if err != nil {
		return AgentToken{}, fmt.Errorf("mint a token for %s: %w", agent, err)
	}
	return answered.agentToken(agent, audience)
}

const (
	// contractHeader names the contract version a route of garam's speaks.
	contractHeader = "Garam-Contract-Version"
	// tokenContract is the contract mintAgentToken speaks (garam@afde8b8).
	tokenContract = "machine-token.v1"
)

// agentTokenPattern is an agent token in garam's form, which is also the whole
// of the file the adapter reads it from (garam@afde8b8 api/machine.yaml
// AgentToken, docs/architecture/adapter.md §Token mode).
var agentTokenPattern = regexp.MustCompile(`^garam_at_[0-9a-f]{64}$`)

// send performs one request against the machine listener, carrying body where
// there is one, and decodes what it answered into out. A route that carries a
// contract version is sent contract, and its answer is read only under the same
// one; an empty contract sends none.
//
// A nil body sends none and a nil out reads none: a route that records
// something answers 204 and has nothing to decode.
func (c *Client) send(ctx context.Context, method, path, contract string, body, out any) error {
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
	if contract != "" {
		request.Header.Set(contractHeader, contract)
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
	if got := response.Header.Get(contractHeader); contract != "" && got != contract {
		return fmt.Errorf("garam answered %d under contract %q, want %q", response.StatusCode, got, contract)
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
//
// A sentinel a method maps a refusal to wraps the refusal beside it, so that
// what counts refusals reads the status through every error a method answers.
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

// tokenRequest is what mintAgentToken is sent: the audience, absent for
// garam's own.
type tokenRequest struct {
	Audience string `json:"audience,omitempty"`
}

// agentTokenPayload is an agent token as the machine listener answers it.
type agentTokenPayload struct {
	Token         string    `json:"token"`
	GRN           string    `json:"grn"`
	Epoch         int64     `json:"epoch"`
	Audience      string    `json:"audience"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ServerRootPEM string    `json:"serverRootPem"`
}

// agentToken is the token answered for agent and audience, once it is one the
// adapter can read: anything else is refused, naming no part of the token.
func (p agentTokenPayload) agentToken(agent GRN, audience string) (AgentToken, error) {
	switch {
	case !agentTokenPattern.MatchString(p.Token):
		return AgentToken{}, fmt.Errorf("a token garam minted for %s is not an agent token", agent)
	case p.GRN != string(agent) || p.Audience != audience:
		return AgentToken{}, fmt.Errorf("garam minted a token for %s and audience %q, not %s and %q",
			p.GRN, p.Audience, agent, audience)
	case p.Epoch < 1 || p.ExpiresAt.IsZero() || p.ServerRootPEM == "":
		return AgentToken{}, fmt.Errorf("a token garam minted for %s carries no epoch, expiry or server root", agent)
	}
	return AgentToken{Token: p.Token, Epoch: p.Epoch, ExpiresAt: p.ExpiresAt, ServerRootPEM: []byte(p.ServerRootPEM)}, nil
}
