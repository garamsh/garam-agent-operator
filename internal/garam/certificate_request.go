package garam

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// NewCertificateRequest generates the key pair this operator authenticates with
// and returns a PKCS#10 request over it. The curve is ECDSA P-256, which is the
// one garam's schema names (garam@b16a896:api/machine.yaml:806-810).
//
// The key is generated here and returned beside the request so that the two
// stay together: an enrollment answers a certificate for this key and no other,
// and garam holds nothing that could answer a second time.
func NewCertificateRequest() (CertificateRequest, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertificateRequest{}, fmt.Errorf("generate the key this operator authenticates to garam with: %w", err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return CertificateRequest{}, fmt.Errorf("build the certificate signing request this operator enrolls with: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return CertificateRequest{}, fmt.Errorf("render the key this operator authenticates to garam with: %w", err)
	}
	return CertificateRequest{
		RequestPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER}),
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// checkSignature runs the check garam runs: the request parses as PKCS#10 and
// its signature proves the sender holds the key it carries.
//
// It runs before the request is sent because the token is spent by the attempt
// whether or not the attempt succeeds, so a request garam would refuse costs
// the token and puts a person back in the console.
func (r CertificateRequest) checkSignature() error {
	block, _ := pem.Decode(r.RequestPEM)
	if block == nil {
		return errors.New("the certificate signing request this operator built holds no PEM block")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("read back the certificate signing request this operator built: %w", err)
	}
	if err := request.CheckSignature(); err != nil {
		return fmt.Errorf("verify the signature on the certificate signing request this operator built: %w", err)
	}
	return nil
}
