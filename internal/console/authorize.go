package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// authorizationScheme is the scheme the console presents its authority under.
const authorizationScheme = "Garam-Operation "

// orgGRNPrefix is the prefix of an organization's GRN, whose last segment the route names.
const orgGRNPrefix = "grn:root:default:org:"

// target is what a request is: the operation it performs on which target, in which organization.
type target struct {
	org       string
	operation string
	grn       string
	// requestTarget, where set, is the one route the authority must be bound to carry.
	requestTarget string
}

// authorize introspects the request's authority and checks every field it binds against the
// request, the body's digest last. It reads nothing out of the body.
func (s *server) authorize(ctx context.Context, r *http.Request, body []byte, want target) (Binding, error) {
	return s.authorizeHeader(ctx, r.Header.Get("Authorization"), body, want)
}

// authorizeHeader is authorize for the authority header carries, as `Garam-Operation <authority>`.
func (s *server) authorizeHeader(ctx context.Context, header string, body []byte, want target) (Binding, error) {
	if !strings.HasPrefix(header, authorizationScheme) || len(header) == len(authorizationScheme) {
		return Binding{}, ErrNoAuthority
	}
	b, err := s.introspector.Introspect(ctx, Authority(strings.TrimPrefix(header, authorizationScheme)))
	if err != nil {
		return Binding{}, err
	}
	switch {
	case !s.now().Before(b.ExpiresAt):
		return Binding{}, ErrAuthorityUnknown
	case b.Audience != s.audience:
		return Binding{}, &MismatchError{Field: "audience"}
	case b.Org != orgGRNPrefix+want.org:
		return Binding{}, &MismatchError{Field: "organization"}
	case b.Operation != want.operation:
		return Binding{}, &MismatchError{Field: "operation"}
	case want.grn != "" && b.Target != want.grn:
		return Binding{}, &MismatchError{Field: "target"}
	case (b.Operation == OperationConfigure || b.Operation == OperationCutover ||
		b.Operation == OperationExecutionRead || b.Operation == OperationRecover) && b.Assignment == nil:
		return Binding{}, &MismatchError{Field: "assignment"}
	case want.requestTarget != "" && b.RequestTarget != want.requestTarget:
		return Binding{}, &MismatchError{Field: "request target"}
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != b.BodySHA256 {
		return Binding{}, ErrDigestMismatch
	}
	return b, nil
}
