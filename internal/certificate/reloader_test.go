package certificate_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/certificate"
)

// writePair writes a self-signed key pair named commonName into dir, stamped at modified.
func writePair(t *testing.T, dir, commonName string, modified time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	require.NoError(t, os.Chtimes(certFile, modified, modified))
	require.NoError(t, os.Chtimes(keyFile, modified, modified))
	return certFile, keyFile
}

func served(t *testing.T, r *certificate.Reloader) string {
	t.Helper()
	c, err := r.GetCertificate(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	require.NoError(t, err)
	return leaf.Subject.CommonName
}

func TestReloader_ServesAReplacedPair(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-time.Hour)
	certFile, keyFile := writePair(t, dir, "first", start)
	r, err := certificate.NewReloader(certFile, keyFile)
	require.NoError(t, err)

	// Control: unchanged files go on serving the pair first loaded.
	assert.Equal(t, "first", served(t, r))

	writePair(t, dir, "second", start.Add(time.Minute))
	assert.Equal(t, "second", served(t, r))
	assert.NoError(t, r.Err())
}

func TestReloader_KeepsServingWhenAReplacementDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-time.Hour)
	certFile, keyFile := writePair(t, dir, "first", start)
	r, err := certificate.NewReloader(certFile, keyFile)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(keyFile, []byte("not a key"), 0o600))
	require.NoError(t, os.Chtimes(keyFile, start.Add(time.Minute), start.Add(time.Minute)))
	assert.Equal(t, "first", served(t, r))
	assert.Error(t, r.Err())

	// Control: a pair that loads replaces it once written.
	writePair(t, dir, "second", start.Add(2*time.Minute))
	assert.Equal(t, "second", served(t, r))
	assert.NoError(t, r.Err())
}

func TestNewReloader_RefusesAPairThatDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir, "first", time.Now())
	require.NoError(t, os.WriteFile(keyFile, []byte("not a key"), 0o600))

	_, err := certificate.NewReloader(certFile, keyFile)
	require.Error(t, err)
}
