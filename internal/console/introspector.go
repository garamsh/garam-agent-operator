package console

import "context"

// Introspector reads what an operation authority binds, from garam, without consuming it.
// It returns ErrAuthorityUnknown, ErrAuthorityForbidden or ErrAuthorityUndecided for the
// answers that refuse; any other error is the introspection's own failure.
type Introspector interface {
	Introspect(ctx context.Context, authority Authority) (Binding, error)
}
