package desired

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// The routes a refusal is counted against, which is the value of the route
// label on refusalsTotal.
const (
	routeDesired = "desired"
	routeStatus  = "status"
)

// requestMargin is how long past a long poll's wait a request may take before
// it is abandoned: the server answers when the wait passes, so the margin
// covers only the network and the server's proofs.
const requestMargin = 30 * time.Second

// Client reaches the control service's controller routes over mutual TLS, as
// this operator's own certificate (ADR 0040).
type Client struct {
	address string
	http    *http.Client
}

// NewClient returns a Client reaching the control service at address, a host
// and port, under tlsConfig.
func NewClient(address string, tlsConfig *tls.Config) *Client {
	return &Client{
		address: address,
		http:    &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}},
	}
}

// The C2 wire, camelCase as ADR 0040 pins it.
type (
	wireAnswer struct {
		Cursor string      `json:"cursor"`
		Agents []wireAgent `json:"agents"`
	}
	wireAgent struct {
		Agent         string            `json:"agent"`
		Revision      string            `json:"revision"`
		Epoch         string            `json:"epoch"`
		Profile       wireProfile       `json:"profile"`
		Configuration wireConfiguration `json:"configuration"`
	}
	wireProfile struct {
		Name             string                      `json:"name"`
		Version          int64                       `json:"version"`
		Resources        corev1.ResourceRequirements `json:"resources"`
		StorageSize      string                      `json:"storageSize"`
		StorageClassName *string                     `json:"storageClassName"`
	}
	wireConfiguration struct {
		Model wireModel         `json:"model"`
		Ego   string            `json:"ego"`
		Tools map[string]string `json:"tools"`
	}
	wireModel struct {
		Provider  string `json:"provider"`
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		APIKeyRef string `json:"apiKeyRef"`
	}
	wireStatus struct {
		ObservedRevision string `json:"observedRevision"`
		RenderedRevision string `json:"renderedRevision"`
	}
	wireError struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
)

// Desired asks for the controller's whole releasable set. With after set, the
// control service waits up to wait for the feed to move past it before
// answering; without it, it answers at once.
//
// A 4xx answer is a *RefusalError. Any other failure — a 500 or 503, an answer
// that is not the wire's, or no answer at all — is returned as it is and is
// worth asking again.
func (c *Client) Desired(ctx context.Context, after string, wait time.Duration) (Answer, error) {
	query := url.Values{"waitSeconds": {strconv.Itoa(int(wait / time.Second))}}
	if after != "" {
		query.Set("after", after)
	}

	ctx, cancel := context.WithTimeout(ctx, wait+requestMargin)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+c.address+"/v1/operators/self/desired?"+query.Encode(), nil)
	if err != nil {
		return Answer{}, fmt.Errorf("build the desired request: %w", err)
	}

	var answer wireAnswer
	if err := c.do(request, routeDesired, &answer); err != nil {
		return Answer{}, err
	}

	agents := make([]Agent, 0, len(answer.Agents))
	for _, agent := range answer.Agents {
		agents = append(agents, Agent{
			GRN: agent.Agent, Revision: agent.Revision, Epoch: agent.Epoch,
			Profile: Profile{
				Name: agent.Profile.Name, Version: agent.Profile.Version, Resources: agent.Profile.Resources,
				StorageSize: agent.Profile.StorageSize, StorageClassName: agent.Profile.StorageClassName,
			},
			Configuration: Configuration{
				Model: Model{
					Provider: agent.Configuration.Model.Provider, BaseURL: agent.Configuration.Model.BaseURL,
					Name: agent.Configuration.Model.Name, APIKeyRef: agent.Configuration.Model.APIKeyRef,
				},
				Ego: agent.Configuration.Ego, Tools: agent.Configuration.Tools,
			},
		})
	}

	return Answer{Cursor: answer.Cursor, Agents: agents}, nil
}

// ReportStatus reports the revision of agent this operator observed and the one
// it rendered. A 4xx answer is a *RefusalError.
func (c *Client) ReportStatus(ctx context.Context, agent, observed, rendered string) error {
	body, err := json.Marshal(wireStatus{ObservedRevision: observed, RenderedRevision: rendered})
	if err != nil {
		return fmt.Errorf("render the status of %s: %w", agent, err)
	}

	ctx, cancel := context.WithTimeout(ctx, requestMargin)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+c.address+"/v1/operators/self/agents/"+url.PathEscape(agent)+"/status", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build the status request for %s: %w", agent, err)
	}
	request.Header.Set("Content-Type", "application/json")

	return c.do(request, routeStatus, nil)
}

// do sends request and decodes a 200 into answer where answer is not nil. A 4xx
// is counted and returned as a *RefusalError.
func (c *Client) do(request *http.Request, route string, answer any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call the control service's %s route: %w", route, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 400 && response.StatusCode < 500 {
		refusal := &RefusalError{Route: route, Status: response.StatusCode}
		var body wireError
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body); err == nil {
			refusal.Kind, refusal.Message = body.Kind, body.Message
		}
		refusalsTotal.WithLabelValues(route, strconv.Itoa(response.StatusCode)).Inc()

		return refusal
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the control service's %s route answered %d", route, response.StatusCode)
	}
	if answer == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(answer); err != nil {
		return fmt.Errorf("decode the control service's %s answer: %w", route, err)
	}

	return nil
}

// asRefusal reports whether err is a refusal of the control service.
func asRefusal(err error) (*RefusalError, bool) {
	var refusal *RefusalError
	ok := errors.As(err, &refusal)

	return refusal, ok
}
