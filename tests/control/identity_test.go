//go:build e2e

package control_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// operatorGRN is the operator GRN the binary reads from its certificate as its audience.
const operatorGRN = "grn:root:default:operator:control-e2e"

// identity names the files the binary's garam flags point at.
type identity struct {
	serverRoot  string
	certificate string
	key         string
}

// writeOperatorIdentity writes a server root and an operator certificate it signed, carrying
// operatorGRN as its one SAN URI, into dir. garam signs none of it: no garam answers this suite.
func writeOperatorIdentity(dir string) (identity, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "control e2e root"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return identity{}, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	san, err := url.Parse(operatorGRN)
	if err != nil {
		return identity{}, err
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{san},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, rootTemplate, &leafKey.PublicKey, rootKey)
	if err != nil {
		return identity{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return identity{}, err
	}
	id := identity{
		serverRoot:  filepath.Join(dir, "server-root.pem"),
		certificate: filepath.Join(dir, "operator.pem"),
		key:         filepath.Join(dir, "operator-key.pem"),
	}
	for path, block := range map[string]*pem.Block{
		id.serverRoot:  {Type: "CERTIFICATE", Bytes: rootDER},
		id.certificate: {Type: "CERTIFICATE", Bytes: leafDER},
		id.key:         {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			return identity{}, err
		}
	}
	return id, nil
}
