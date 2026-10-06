package garam_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// projectingStore writes each credential where the kubelet projects the
// operator's Secret, which is what a deployed enroller's write reaches.
type projectingStore struct {
	certificateFile, keyFile string
}

func (s projectingStore) ReplaceCredential(_ context.Context, credential garam.Credential) error {
	if err := os.WriteFile(s.keyFile, credential.KeyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(s.certificateFile, credential.CertificatePEM, 0o600)
}

// TestAnEnrollingOperatorPullsFromTheControlServiceOnceItHasEnrolled is #267: an
// operator given --control-address and only an enrollment token starts, enrolls,
// and then pulls its desired state, without a restart.
//
// The control is the configuration an operator that is not enrolling takes,
// refusing the same absent pair at startup, which is what exited the manager
// before its enroller ran.
func TestAnEnrollingOperatorPullsFromTheControlServiceOnceItHasEnrolled(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	dir := t.TempDir()
	certificateFile, keyFile := dir+"/certificate.pem", dir+"/key.pem"
	control := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusOK, `{"cursor":"1","agents":[]}`)
	})

	_, err := garam.OperatorTLS(false, certificateFile, keyFile, control.trustFile)
	g.Expect(err).To(MatchError(ContainSubstring("read the certificate this operator authenticates to garam with")))

	tlsConfig, err := garam.OperatorTLS(true, certificateFile, keyFile, control.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	client := desired.NewClient(control.address(), tlsConfig)
	_, err = client.Desired(ctx, "", 0)
	g.Expect(err).To(HaveOccurred(), "a pull before enrollment has no certificate to present")
	g.Expect(control.requests()).To(BeEmpty())

	stopped := startEnroller(t, ctx, newEnroller(t, newEnrollmentStub(t),
		projectingStore{certificateFile: certificateFile, keyFile: keyFile}, "a-token", dir))
	g.Eventually(stopped, 10*time.Second).Should(BeClosed())

	answer, err := client.Desired(ctx, "", 0)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(answer.Cursor).To(Equal("1"))
	g.Expect(control.requests()).To(HaveLen(1))
	g.Expect(control.requests()[0].client).To(Equal(enrolledOperator), "the pull presents the enrolled certificate")
}
