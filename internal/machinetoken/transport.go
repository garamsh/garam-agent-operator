package machinetoken

import (
	"net/http"
)

// Transport presents a Source's token as `Authorization: Bearer` on every request it carries. A
// request refused with 401 is sent once more under a token from a fresh exchange, where the Source
// may exchange and the body can be sent again; otherwise the 401 is answered as it came.
type Transport struct {
	Source *Source
	// Base sends the request; it verifies garam's, or the control service's, server certificate.
	Base http.RoundTripper
	// ClientIDHeader, where set, names a header carrying the client_id the token presented was
	// exchanged under, so the two are always one identity's.
	ClientIDHeader string
}

// RoundTrip sends request under the token held.
func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	token, clientID, err := t.Source.credential(request.Context())
	if err != nil {
		closeBody(request)
		return nil, err
	}
	response, err := t.Base.RoundTrip(t.authorized(request, token, clientID))
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	if request.Body != nil && request.GetBody == nil {
		return response, nil
	}
	t.Source.Invalidate(token)
	fresh, freshClientID, err := t.Source.credential(request.Context())
	if err != nil || fresh == token {
		return response, nil
	}
	retry := t.authorized(request, fresh, freshClientID)
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return response, nil
		}
		retry.Body = body
	}
	_ = response.Body.Close()
	return t.Base.RoundTrip(retry)
}

// authorized is a copy of request carrying token, so the caller's request is never changed.
func (t *Transport) authorized(request *http.Request, token, clientID string) *http.Request {
	out := request.Clone(request.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	if t.ClientIDHeader != "" {
		out.Header.Set(t.ClientIDHeader, clientID)
	}
	return out
}

// closeBody closes a body that is never sent, as a RoundTripper must.
func closeBody(request *http.Request) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
}
