package garam

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// ServerTLS returns the TLS configuration this operator reaches garam's machine
// listener with: the listener is verified against trustFile alone, and nothing
// is presented, because the token on each request is what authenticates it.
//
// trustFile is a path so that what this operator trusts is the deployment's to
// supply.
func ServerTLS(trustFile string) (*tls.Config, error) {
	trusted, err := os.ReadFile(trustFile)
	if err != nil {
		return nil, fmt.Errorf("read the file garam is verified against: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trusted) {
		return nil, fmt.Errorf("%s holds no PEM certificate for garam to be verified against", trustFile)
	}
	return &tls.Config{
		// garam's machine listener serves TLS 1.3 and nothing older
		// (garam@8f9dd9d:internal/machine/listener.go:26).
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
	}, nil
}
