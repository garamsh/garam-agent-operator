package token

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"time"

	"github.com/garamsh/garam/lib/errorx"
)

const (
	// assertionLifetime is the longest an assertion may live (ADR-0151
	// §6): garam refuses `exp` more than 60 seconds after `iat`.
	assertionLifetime = 60 * time.Second

	minModulusBits = 2048
	maxModulusBits = 4096
)

// parseKey parses the PEM PKCS#8 RSA key garam issues.
func parseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errorx.New(errorx.KindInvalidArgument, "operator key is not a PEM PKCS#8 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errorx.Wrap(err, "parse operator key", errorx.KindInvalidArgument)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errorx.New(errorx.KindInvalidArgument, "operator key is not RSA")
	}
	if bits := key.N.BitLen(); bits < minModulusBits || bits > maxModulusBits {
		return nil, errorx.New(errorx.KindInvalidArgument, "operator key modulus is outside 2048 to 4096 bits")
	}
	return key, nil
}

// thumbprint is the key's RFC 7638 SHA-256 thumbprint, base64url
// without padding: the `kid` garam knows the key by.
func thumbprint(pub *rsa.PublicKey) string {
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// assertion signs the RFC 7523 assertion the operator clientID presents
// to the token endpoint at tokenURL: RS256, `typ` JWT, `aud` the token
// URL as issued, and a `jti` that is fresh because garam accepts each
// assertion once.
func assertion(key *rsa.PrivateKey, clientID, tokenURL string, now time.Time) (string, error) {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", errorx.Wrap(err, "assertion jti", errorx.KindInternal)
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": thumbprint(&key.PublicKey), "typ": "JWT"})
	if err != nil {
		return "", errorx.Wrap(err, "assertion header", errorx.KindInternal)
	}
	claims, err := json.Marshal(map[string]any{
		"iss": clientID,
		"sub": clientID,
		"aud": tokenURL,
		"iat": now.Unix(),
		"exp": now.Add(assertionLifetime).Unix(),
		"jti": base64.RawURLEncoding.EncodeToString(jti),
	})
	if err != nil {
		return "", errorx.Wrap(err, "assertion claims", errorx.KindInternal)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", errorx.Wrap(err, "sign assertion", errorx.KindInternal)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
