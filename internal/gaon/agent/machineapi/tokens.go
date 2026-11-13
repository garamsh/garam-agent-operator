package machineapi

import "context"

// Tokens is the operator's bearer token for garam's own routes.
type Tokens interface {
	// Token returns a live token.
	Token(ctx context.Context) (string, error)
	// Invalidate drops the held token, so the next Token exchanges
	// again.
	Invalidate()
}
