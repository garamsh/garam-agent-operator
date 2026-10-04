//go:build e2e

package control_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// pemCertificate and pemECKey are the PEM block types the binary's certificate flags read.
const (
	pemCertificate = "CERTIFICATE"
	pemECKey       = "EC PRIVATE KEY"
)

// operatorGRN is the operator GRN the binary reads from its certificate as its audience.
const operatorGRN = "grn:root:default:operator:control-e2e"

// identity names the files the binary's certificate flags point at, and the root that signed them.
type identity struct {
	serverRoot         string
	certificate        string
	key                string
	servingCertificate string
	servingKey         string
	roots              *x509.CertPool
	// controller is a controller's client certificate, carrying controllerGRN as its one SAN URI.
	controller tls.Certificate
}

// controllerGRN is the operator GRN the controller certificate names.
const controllerGRN = "grn:root:default:operator:k8s-e2e"

// writeIdentity writes into dir a root and two certificates it signed: an operator certificate
// carrying operatorGRN as its one SAN URI, and a serving certificate for 127.0.0.1, under which
// the binary serves the console's routes. garam signs none of it: no garam answers this suite.
func writeIdentity(dir string) (identity, error) {
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
	rootCert, err := x509.ParseCertificate(rootDER)
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
	servingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	servingDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, rootTemplate, &servingKey.PublicKey, rootKey)
	if err != nil {
		return identity{}, err
	}
	servingKeyDER, err := x509.MarshalECPrivateKey(servingKey)
	if err != nil {
		return identity{}, err
	}
	id := identity{
		serverRoot:         filepath.Join(dir, "server-root.pem"),
		certificate:        filepath.Join(dir, "operator.pem"),
		key:                filepath.Join(dir, "operator-key.pem"),
		servingCertificate: filepath.Join(dir, "serving.pem"),
		servingKey:         filepath.Join(dir, "serving-key.pem"),
		roots:              x509.NewCertPool(),
	}
	id.roots.AddCert(rootCert)
	controllerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	controllerSAN, err := url.Parse(controllerGRN)
	if err != nil {
		return identity{}, err
	}
	controllerDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(4),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{controllerSAN},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, rootTemplate, &controllerKey.PublicKey, rootKey)
	if err != nil {
		return identity{}, err
	}
	id.controller = tls.Certificate{Certificate: [][]byte{controllerDER}, PrivateKey: controllerKey}
	for path, block := range map[string]*pem.Block{
		id.serverRoot:         {Type: pemCertificate, Bytes: rootDER},
		id.certificate:        {Type: pemCertificate, Bytes: leafDER},
		id.key:                {Type: pemECKey, Bytes: keyDER},
		id.servingCertificate: {Type: pemCertificate, Bytes: servingDER},
		id.servingKey:         {Type: pemECKey, Bytes: servingKeyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			return identity{}, err
		}
	}
	return id, nil
}
