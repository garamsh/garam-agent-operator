package garam

import "context"

// CredentialStore is where the credential this operator authenticates to garam
// with is kept, and is what a renewal is written through.
//
// It is one place rather than two on purpose. The credential is read as files
// and replaced through this, and both reach the same store, so nothing copies
// it from one home to another and nothing can re-seed it over a renewal.
type CredentialStore interface {
	// ReplaceCredential puts credential where the next handshake reads it.
	// garam keeps no private key and has already moved the certificate it
	// renewed, so a credential this fails to store is not obtainable again.
	ReplaceCredential(ctx context.Context, credential Credential) error
}
