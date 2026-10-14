package machinetoken

import (
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// assertionLifetime is exp - iat; garam refuses more than 60 seconds.
const assertionLifetime = 60 * time.Second

// jtiBytes is the random length of an assertion's jti: 256 bits, 43 base64url characters, inside
// garam's 22 to 128.
const jtiBytes = 32

// identity is one read of the three files: the key an exchange signs with, and the values it names.
type identity struct {
	key      *rsa.PrivateKey
	clientID string
	tokenURL string
}

// readIdentity reads the three files once. A key is never part of what it reports.
func readIdentity(files Files) (identity, error) {
	clientID, err := readValue(files.ClientID, "client_id")
	if err != nil {
		return identity{}, err
	}
	tokenURL, err := readValue(files.TokenURL, "tokenUrl")
	if err != nil {
		return identity{}, err
	}
	encoded, err := os.ReadFile(files.PrivateKey)
	if err != nil {
		return identity{}, fmt.Errorf("read the private key file: %w", err)
	}
	block, _ := pem.Decode(encoded)
	if block == nil || block.Type != "PRIVATE KEY" {
		return identity{}, errors.New("the private key file holds no PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// The parser's error names a structure, never key material.
		return identity{}, fmt.Errorf("parse the private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return identity{}, errors.New("the private key is not an RSA key, and garam verifies RS256 alone")
	}
	return identity{key: key, clientID: clientID, tokenURL: tokenURL}, nil
}

// ReadClientID reads the client_id file: the operator GRN its tokens are exchanged under.
func ReadClientID(path string) (string, error) {
	return readValue(path, "client_id")
}

// readValue reads a one-line file, without the line end a deployer's tooling may have added.
func readValue(path, name string) (string, error) {
	read, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the %s file: %w", name, err)
	}
	value := strings.TrimRight(string(read), "\r\n")
	if value == "" {
		return "", fmt.Errorf("the %s file is empty", name)
	}
	return value, nil
}

// sign makes one assertion over id's key, naming the kid of that same key, a jti fresh from random,
// and iat at now.
func sign(id identity, now time.Time, random io.Reader) (string, error) {
	kid, err := thumbprint(&id.key.PublicKey)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, jtiBytes)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return "", fmt.Errorf("draw an assertion's jti: %w", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: id.key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("prepare the assertion's signer: %w", err)
	}
	issued := now.Truncate(time.Second)
	return jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   id.clientID,
		Subject:  id.clientID,
		Audience: jwt.Audience{id.tokenURL},
		IssuedAt: jwt.NewNumericDate(issued),
		Expiry:   jwt.NewNumericDate(issued.Add(assertionLifetime)),
		ID:       base64.RawURLEncoding.EncodeToString(nonce),
	}).Serialize()
}

// thumbprint is the key's RFC 7638 SHA-256 thumbprint, base64url without padding.
func thumbprint(public *rsa.PublicKey) (string, error) {
	sum, err := (&jose.JSONWebKey{Key: public}).Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("compute the key's thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}
