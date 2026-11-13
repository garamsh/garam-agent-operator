// Package machineapi is garam's machine listener as the reconciler uses
// it: mintAgentToken and reportProvisioningState over the client
// generated from platform/api/machine.yaml, each under the operator's
// bearer token.
package machineapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/guest"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
	"github.com/garamsh/garam/lib/platform-sdk-go/machine"
)

// Secret keys of the two tokens an agent holds.
const (
	// KeyAgentToken is the token garam accepts, for the agent's adapter.
	KeyAgentToken = guest.AgentTokenFile
	// KeyControlToken is the agent token minted with the control service
	// as audience, which the adapter presents to the lifecycle authority.
	KeyControlToken = guest.ControlTokenFile
)

// tokenContract is the contract version mintAgentToken is called under.
const tokenContract = "machine-token.v1"

// Client implements agent.Garam.
type Client struct {
	api     *machine.ClientWithResponses
	tokens  Tokens
	control string
}

var _ agent.Garam = (*Client)(nil)

// New returns the Client of the machine listener at url, calling as the
// holder of tokens. control is the control service's GRN, the audience
// of the control token.
func New(url string, httpClient *http.Client, tokens Tokens, control string) (*Client, error) {
	c := &Client{tokens: tokens, control: control}
	api, err := machine.NewClientWithResponses(url,
		machine.WithHTTPClient(httpClient),
		machine.WithRequestEditorFn(c.authorize))
	if err != nil {
		return nil, errorx.Wrap(err, "build the machine client", errorx.KindInvalidArgument)
	}
	c.api = api
	return c, nil
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	t, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t)
	return nil
}

// MintTokens mints the agent's two tokens: garam's own and the one
// whose audience is the control service. A token minted at another
// epoch than the feed's is refused as stale.
func (c *Client) MintTokens(ctx context.Context, g grn.GRN, epoch int64) (agent.Credentials, error) {
	own, err := c.mint(ctx, g, epoch, nil)
	if err != nil {
		return nil, err
	}
	control, err := c.mint(ctx, g, epoch, &c.control)
	if err != nil {
		return nil, err
	}
	return agent.Credentials{KeyAgentToken: []byte(own), KeyControlToken: []byte(control)}, nil
}

func (c *Client) mint(ctx context.Context, g grn.GRN, epoch int64, audience *string) (string, error) {
	params := &machine.MintAgentTokenParams{TokenContractVersion: tokenContract}
	body := machine.TokenExchange{Audience: audience}
	res, err := c.api.MintAgentTokenWithResponse(ctx, g.String(), params, body)
	if err == nil && res.StatusCode() == http.StatusUnauthorized {
		c.tokens.Invalidate()
		res, err = c.api.MintAgentTokenWithResponse(ctx, g.String(), params, body)
	}
	if err != nil {
		return "", errorx.Wrap(err, "mint agent token", errorx.KindUnavailable)
	}
	switch status := res.StatusCode(); status {
	case http.StatusCreated:
		t := res.JSON201
		if t == nil || t.Token == "" {
			return "", errorx.New(errorx.KindInternal, "garam answered 201 without a token")
		}
		if int64(t.Epoch) != epoch {
			return "", errorx.Wrap(agent.ErrStaleEpoch,
				"token minted at epoch "+strconv.Itoa(t.Epoch)+", feed holds "+strconv.FormatInt(epoch, 10))
		}
		return t.Token, nil
	case http.StatusBadRequest:
		return "", refusal("mint agent token", errorx.KindInvalidArgument, res.JSON400)
	case http.StatusUnauthorized:
		return "", refusal("mint agent token", errorx.KindUnauthenticated, res.JSON401)
	case http.StatusForbidden:
		return "", refusal("mint agent token", errorx.KindPermissionDenied, res.JSON403)
	default:
		return "", unexpected("mint agent token", status)
	}
}

// ReportState reports the provisioning state. A 409 failed_precondition
// is garam's answer for an epoch the agent has left, and is returned as
// agent.ErrStaleEpoch.
func (c *Client) ReportState(ctx context.Context, g grn.GRN, epoch int64, s agent.State) error {
	body := machine.ProvisioningStateReport{Epoch: int(epoch), State: machine.ProvisioningState(s)}
	res, err := c.api.ReportProvisioningStateWithResponse(ctx, g.String(), body)
	if err == nil && res.StatusCode() == http.StatusUnauthorized {
		c.tokens.Invalidate()
		res, err = c.api.ReportProvisioningStateWithResponse(ctx, g.String(), body)
	}
	if err != nil {
		return errorx.Wrap(err, "report provisioning state", errorx.KindUnavailable)
	}
	switch status := res.StatusCode(); status {
	case http.StatusNoContent:
		return nil
	case http.StatusBadRequest:
		return refusal("report provisioning state", errorx.KindInvalidArgument, res.JSON400)
	case http.StatusUnauthorized:
		return refusal("report provisioning state", errorx.KindUnauthenticated, res.JSON401)
	case http.StatusForbidden:
		return refusal("report provisioning state", errorx.KindPermissionDenied, res.JSON403)
	case http.StatusConflict:
		if res.JSON409 != nil && res.JSON409.Kind == errorx.KindFailedPrecondition.String() {
			return errorx.Wrap(agent.ErrStaleEpoch, "garam refused the report: "+res.JSON409.Message)
		}
		return refusal("report provisioning state", errorx.KindAborted, res.JSON409)
	default:
		return unexpected("report provisioning state", status)
	}
}

func refusal(op string, kind errorx.Kind, e *machine.Error) error {
	if e == nil {
		return errorx.New(kind, op+": garam refused with an unreadable body")
	}
	return errorx.New(kind, op+": garam refused ("+e.Kind+"): "+e.Message)
}

func unexpected(op string, status int) error {
	kind := errorx.KindInternal
	if status >= http.StatusInternalServerError {
		kind = errorx.KindUnavailable
	}
	return errorx.New(kind, op+": garam answered "+strconv.Itoa(status))
}
