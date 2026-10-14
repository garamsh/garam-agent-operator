//go:build e2e

package control_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"maps"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// pemCertificate, pemECKey and pemPrivateKey are the PEM block types the binary's file flags read.
const (
	pemCertificate = "CERTIFICATE"
	pemECKey       = "EC PRIVATE KEY"
	pemPrivateKey  = "PRIVATE KEY"
)

// operatorGRN is the operator GRN the binary reads from its client_id file as its audience.
const operatorGRN = "grn:root:default:operator:control-e2e"

// identity names the files the binary's flags point at, and the root that signed its serving
// certificate.
type identity struct {
	serverRoot         string
	operatorKeys       machinetoken.Files
	servingCertificate string
	servingKey         string
	roots              *x509.CertPool
}

// controllerGRN is the operator GRN the detached binary's controller client names.
const controllerGRN = "grn:root:default:operator:k8s-e2e"

// controllerToken is a token of garam's operator form that no garam issued: the detached binary has
// nothing to prove it with.
const controllerToken = "garam_ot_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// writeIdentity writes into dir a root, the serving certificate it signed for 127.0.0.1, under
// which the binary serves the console's routes, and a key issuance naming operatorGRN. garam signs
// and issues none of it: no garam answers the detached binary.
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
		servingCertificate: filepath.Join(dir, "serving.pem"),
		servingKey:         filepath.Join(dir, "serving-key.pem"),
		roots:              x509.NewCertPool(),
	}
	id.roots.AddCert(rootCert)
	if id.operatorKeys, err = writeKeyIssuance(dir, "operator", operatorGRN, garamURL+"/oauth/token",
		unissuedKey()); err != nil {
		return identity{}, err
	}
	for path, block := range map[string]*pem.Block{
		id.serverRoot:         {Type: pemCertificate, Bytes: rootDER},
		id.servingCertificate: {Type: pemCertificate, Bytes: servingDER},
		id.servingKey:         {Type: pemECKey, Bytes: servingKeyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			return identity{}, err
		}
	}
	return id, nil
}

// unissuedKey is a PKCS#8 RSA key garam never issued, for a binary that never reaches a garam.
func unissuedKey() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: der}))
}

// writeKeyIssuance writes into dir, under prefix, the three files a key issuance places, as a
// deployer places what garam's issueOperatorKey answered.
func writeKeyIssuance(dir, prefix, clientID, tokenURL, privateKeyPEM string) (machinetoken.Files, error) {
	files := machinetoken.Files{
		PrivateKey: filepath.Join(dir, prefix+"-private-key.pem"),
		ClientID:   filepath.Join(dir, prefix+"-client-id"),
		TokenURL:   filepath.Join(dir, prefix+"-token-url"),
	}
	for path, content := range map[string]string{
		files.PrivateKey: privateKeyPEM, files.ClientID: clientID, files.TokenURL: tokenURL,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return machinetoken.Files{}, err
		}
	}
	return files, nil
}

// bearerClient is a client trusting roots that presents token under grn, as the manager presents its
// token to the controller routes.
func bearerClient(roots *x509.CertPool, grn, token string) *http.Client {
	return &http.Client{Transport: presenting{
		base:   &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
		header: http.Header{"Authorization": {"Bearer " + token}, desired.ControllerHeader: {grn}},
	}}
}

// presenting sends every request with header set on it.
type presenting struct {
	base   http.RoundTripper
	header http.Header
}

func (p presenting) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	maps.Copy(r.Header, p.header)
	return p.base.RoundTrip(r)
}
