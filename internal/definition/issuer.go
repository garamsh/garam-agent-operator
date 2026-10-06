package definition

import "context"

// Issuer asks garam for an agent's first certificate. A definite refusal is an
// *IssuanceRefusedError; an answer garam left undecided wraps ErrIssuanceUndecided. garam answers
// one request with one stored result however often it is sent.
type Issuer interface {
	Issue(ctx context.Context, i Issuance) (IssuedCertificate, error)
}
