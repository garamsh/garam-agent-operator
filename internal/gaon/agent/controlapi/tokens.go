package controlapi

import "context"

// Tokens is the operator's bearer token for the control service: the
// one whose audience is the control service, which garam accepts on no
// route of its own.
type Tokens interface {
	// Token returns a live token.
	Token(ctx context.Context) (string, error)
	// Invalidate drops the held token, so the next Token exchanges
	// again.
	Invalidate()
}
