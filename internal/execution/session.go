package execution

import (
	"encoding/pem"
	"net/http"
)

// agentLeaf is the leaf the agent presented in this request's handshake, as one PEM block, once
// it names the agent in the path as its one SAN URI. garam decides per request whether it is the
// agent's current credential; nothing here verifies its chain.
func agentLeaf(r *http.Request, agent string) ([]byte, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, errUnauthenticated
	}
	leaf := r.TLS.PeerCertificates[0]
	if len(leaf.URIs) != 1 {
		return nil, errUnauthenticated
	}
	if leaf.URIs[0].String() != agent {
		return nil, errAnotherAgent
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), nil
}
