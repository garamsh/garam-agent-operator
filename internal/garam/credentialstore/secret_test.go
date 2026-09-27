package credentialstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/garam/credentialstore"
)

// patchedSecret is one patch the store sent, as the API server would receive
// it: the object named, the patch's type, and its body.
type patchedSecret struct {
	name  types.NamespacedName
	kind  types.PatchType
	body  map[string]map[string][]byte
	count int
}

// newClientRecordingPatches substitutes the API server, which is the boundary
// this unit is written against.
func newClientRecordingPatches(t *testing.T, recorded *patchedSecret) client.Client {
	t.Helper()

	return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(_ context.Context, _ client.WithWatch, obj client.Object,
			patch client.Patch, _ ...client.PatchOption) error {
			body, err := patch.Data(obj)
			if err != nil {
				return err
			}
			recorded.count++
			recorded.name = client.ObjectKeyFromObject(obj)
			recorded.kind = patch.Type()
			return json.Unmarshal(body, &recorded.body)
		},
	}).Build()
}

// TestReplaceCredentialWritesBothKeysInOnePatch is the property a reader
// depends on: a handshake reads the certificate beside the key it was issued
// with, never beside another's.
func TestReplaceCredentialWritesBothKeysInOnePatch(t *testing.T) {
	g := NewWithT(t)
	recorded := &patchedSecret{}
	secret := types.NamespacedName{Namespace: "garam-agent-operator-system", Name: "garam-credential"}

	store := credentialstore.NewSecret(
		newClientRecordingPatches(t, recorded), secret, "certificate.pem", "key.pem")
	err := store.ReplaceCredential(context.Background(), garam.Credential{
		CertificatePEM: []byte("a certificate"),
		KeyPEM:         []byte("a key"),
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(recorded.count).To(Equal(1))
	g.Expect(recorded.name).To(Equal(secret))
	g.Expect(recorded.kind).To(Equal(types.MergePatchType))
	g.Expect(recorded.body["data"]).To(Equal(map[string][]byte{
		"certificate.pem": []byte("a certificate"),
		"key.pem":         []byte("a key"),
	}))
}

// TestReplaceCredentialWritesTheKeysItWasGiven says the names come from the
// deployment's own file paths rather than from a constant here.
func TestReplaceCredentialWritesTheKeysItWasGiven(t *testing.T) {
	g := NewWithT(t)
	recorded := &patchedSecret{}

	store := credentialstore.NewSecret(newClientRecordingPatches(t, recorded),
		types.NamespacedName{Namespace: "other", Name: "elsewhere"}, "tls.crt", "tls.key")
	err := store.ReplaceCredential(context.Background(), garam.Credential{
		CertificatePEM: []byte("a certificate"),
		KeyPEM:         []byte("a key"),
	})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(recorded.body["data"]).To(HaveKey("tls.crt"))
	g.Expect(recorded.body["data"]).To(HaveKey("tls.key"))
}

// TestReplaceCredentialReportsWhatTheAPIRefused says a lost renewal is reported
// rather than swallowed: garam keeps no private key, so what this fails to
// write is not obtainable again.
func TestReplaceCredentialReportsWhatTheAPIRefused(t *testing.T) {
	g := NewWithT(t)

	refusing := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(context.Context, client.WithWatch, client.Object,
			client.Patch, ...client.PatchOption) error {
			return errAPIRefusal
		},
	}).Build()

	store := credentialstore.NewSecret(refusing,
		types.NamespacedName{Namespace: "garam-agent-operator-system", Name: "garam-credential"},
		"certificate.pem", "key.pem")
	err := store.ReplaceCredential(context.Background(), garam.Credential{
		CertificatePEM: []byte("a certificate"),
		KeyPEM:         []byte("a key"),
	})

	g.Expect(err).To(MatchError(errAPIRefusal))
	g.Expect(err).To(MatchError(ContainSubstring("garam-credential")))
}

// errAPIRefusal stands in for whatever the API server answers a patch it will not
// take.
var errAPIRefusal = errors.New("the API server refused the patch")
