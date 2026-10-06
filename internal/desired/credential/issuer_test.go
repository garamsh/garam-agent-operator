package credential_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/controller"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/desired/credential"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// namespace is the spec's own: an issuer serves every managed agent in its
// namespace, so an agent one spec leaves unplaced would otherwise be served, and
// asked for, by the next spec's issuer.
var namespace string

var _ = BeforeEach(func() {
	namespaces++
	namespace = fmt.Sprintf("issuer-%d", namespaces)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
})

var namespaces int

// routeRequest is one certificate request the route double received.
type routeRequest struct {
	agent, requestID, epoch, csr string
}

// routeDouble stands in for the control service's certificate-request route as
// #218 pins it: it signs a request's key with a test authority, answers 201 the
// first time and 200 with the same body to an identical retry, refuses a request
// id reused for another body or epoch with 409 request_reused, and a request
// whose epoch is not current with 409 epoch_superseded, recording nothing for
// either refusal. refuse answers every request with a status instead, and drop
// closes the connection after recording the first request, as a lost answer.
type routeDouble struct {
	mu       sync.Mutex
	epoch    string
	refuse   int
	drop     bool
	received []routeRequest
	answered map[string]wireAnswer
	bodies   map[string]routeRequest
	issued   int
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	server   *httptest.Server
}

type wireAnswer struct {
	Agent          string    `json:"agent"`
	Epoch          string    `json:"epoch"`
	CertificatePEM string    `json:"certificatePem"`
	IssuerPEM      string    `json:"issuerPem"`
	ServerRootPEM  string    `json:"serverRootPem"`
	NotAfter       time.Time `json:"notAfter"`
}

func (r *routeDouble) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	defer GinkgoRecover()
	var body struct {
		RequestID             string `json:"requestId"`
		Epoch                 string `json:"epoch"`
		CertificateRequestPEM string `json:"certificateRequestPem"`
	}
	Expect(json.NewDecoder(req.Body).Decode(&body)).To(Succeed())
	agent := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/operators/self/agents/"), "/certificate-requests")
	got := routeRequest{agent: agent, requestID: body.RequestID, epoch: body.Epoch, csr: body.CertificateRequestPEM}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, got)
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.refuse != 0:
		write(r.refuse, refusal("not_authorized"))
	case body.Epoch != r.epoch:
		write(http.StatusConflict, refusal("epoch_superseded"))
	case r.bodies[body.RequestID] != (routeRequest{}) && r.bodies[body.RequestID] != got:
		write(http.StatusConflict, refusal("request_reused"))
	case r.bodies[body.RequestID] == got:
		write(http.StatusOK, r.answered[body.RequestID])
	default:
		answer := r.sign(agent, body.Epoch, body.CertificateRequestPEM)
		r.bodies[body.RequestID], r.answered[body.RequestID] = got, answer
		r.issued++
		if r.drop {
			r.drop = false
			hijacked, _, err := w.(http.Hijacker).Hijack()
			Expect(err).NotTo(HaveOccurred())
			_ = hijacked.Close()

			return
		}
		write(http.StatusCreated, answer)
	}
}

// refusal is the error body the route answers a refusal with.
func refusal(kind string) map[string]string {
	return map[string]string{"kind": kind, "message": "refused"}
}

// sign is the certificate a real issuer would answer: the request's key, signed
// by the test authority.
func (r *routeDouble) sign(agent, epoch, csrPEM string) wireAnswer {
	block, _ := pem.Decode([]byte(csrPEM))
	Expect(block).NotTo(BeNil())
	request, err := x509.ParseCertificateRequest(block.Bytes)
	Expect(err).NotTo(HaveOccurred())
	Expect(request.CheckSignature()).To(Succeed())
	template := &x509.Certificate{
		SerialNumber: big.NewInt(int64(r.issued + 2)), Subject: pkix.Name{},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour).Truncate(time.Second),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, r.ca, request.PublicKey, r.caKey)
	Expect(err).NotTo(HaveOccurred())
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.ca.Raw}))

	return wireAnswer{
		Agent: agent, Epoch: epoch,
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		IssuerPEM:      caPEM, ServerRootPEM: caPEM, NotAfter: template.NotAfter.UTC(),
	}
}

func (r *routeDouble) requests() []routeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]routeRequest(nil), r.received...)
}

func (r *routeDouble) set(change func(*routeDouble)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

// serveRoute starts the route double at epoch, and stops it when the spec ends.
func serveRoute(epoch string) *routeDouble {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())
	ca, err := x509.ParseCertificate(caDER)
	Expect(err).NotTo(HaveOccurred())

	route := &routeDouble{epoch: epoch, answered: map[string]wireAnswer{}, bodies: map[string]routeRequest{}, ca: ca, caKey: caKey}
	route.server = httptest.NewTLSServer(route)
	DeferCleanup(route.server.Close)

	return route
}

// issuer is an issuer reaching route and persisting through store.
func issuer(route *routeDouble, store desired.CredentialStore) *desired.Issuer {
	roots := x509.NewCertPool()
	roots.AddCert(route.server.Certificate())
	return desired.NewIssuer(desired.NewClient(route.server.Listener.Addr().String(),
		&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), store)
}

// run runs i until stop is called or the spec ends, and returns stop, which
// waits for it to end.
func run(i *desired.Issuer) func() {
	runCtx, cancelRun := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		_ = i.Start(runCtx)
		close(stopped)
	}()
	stop := func() {
		cancelRun()
		<-stopped
	}
	DeferCleanup(stop)

	return stop
}

// managedAgent creates the Agent the feed would construct for grn: on the
// control source, at epoch 7, with no credential yet.
func managedAgent(grn string) {
	GinkgoHelper()

	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(grn), Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 "example.com/sherlock:v1",
			CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize:           resource.MustParse("1Gi"),
			Identity: &agentv1alpha1.AgentIdentity{
				GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl,
			},
		},
	}
	Expect(k8sClient.Create(ctx, agent)).To(Succeed())
}

// secret reads a Secret's data, and reports false where it does not exist.
func secret(name string) (map[string][]byte, bool) {
	GinkgoHelper()

	read := &corev1.Secret{}
	err := k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, read)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	Expect(err).NotTo(HaveOccurred())

	return read.Data, true
}

// placedCredential waits for grn's credential Secret and returns its data.
func placedCredential(grn string) map[string][]byte {
	GinkgoHelper()

	var data map[string][]byte
	Eventually(func() bool {
		var placed bool
		data, placed = secret(agentname.CredentialsSecret(grn))
		return placed
	}, 30*time.Second, 100*time.Millisecond).Should(BeTrue(), "the credential is placed")

	return data
}

// expectPair asserts that the placed key is the one the certificate was signed
// over, which is what makes it a credential at all.
func expectPair(data map[string][]byte) {
	GinkgoHelper()

	_, err := tls.X509KeyPair(data[garam.CertificateKey], data[garam.KeyKey])
	Expect(err).NotTo(HaveOccurred())
}

// crashAfterSave is a store that stops the issuer once a request is persisted,
// before it can be sent: a manager killed between the two steps.
type crashAfterSave struct {
	desired.CredentialStore
	crash func()
	once  sync.Once
}

func (c *crashAfterSave) SaveRequest(ctx context.Context, agent string, request desired.PendingRequest) error {
	err := c.CredentialStore.SaveRequest(ctx, agent, request)
	c.once.Do(c.crash)

	return err
}

func newStore() *credential.Secrets {
	return credential.NewSecrets(k8sClient, k8sClient, scheme.Scheme, namespace)
}

var _ = Describe("Managed credential issuer", func() {
	It("persists the key and request before sending, and a restart sends that same request and places its pair", func() {
		grn := "grn:acme:default:agent:1111111111111111"
		managedAgent(grn)
		route := serveRoute("7")

		By("crashing the first issuer right after it persisted its request")
		crashCtx, crash := context.WithCancel(ctx)
		crashed := issuer(route, &crashAfterSave{CredentialStore: newStore(), crash: crash})
		stopped := make(chan struct{})
		go func() { _ = crashed.Start(crashCtx); close(stopped) }()
		Eventually(stopped, 10*time.Second).Should(BeClosed())
		persisted, found := secret(agentname.CredentialRequestSecret(grn))
		Expect(found).To(BeTrue(), "the request is persisted")
		Expect(persisted).To(HaveKey(garam.KeyKey))
		Expect(string(persisted["epoch"])).To(Equal("7"))

		By("restarting, which loads the persisted request rather than making another")
		run(issuer(route, newStore()))
		data := placedCredential(grn)
		Expect(data[garam.KeyKey]).To(Equal(persisted[garam.KeyKey]), "the persisted key is the one placed")
		expectPair(data)
		for _, received := range route.requests() {
			Expect(received.requestID).To(Equal(string(persisted["request-id"])))
			Expect(received.csr).To(Equal(string(persisted["request.pem"])))
		}

		By("the request is removed once the credential is placed")
		Eventually(func() bool { _, found := secret(agentname.CredentialRequestSecret(grn)); return found }).Should(BeFalse())
	})

	It("asks again with the same request after an answer lost in flight, and places the stored result", func() {
		grn := "grn:acme:default:agent:2222222222222222"
		managedAgent(grn)
		route := serveRoute("7")
		route.set(func(r *routeDouble) { r.drop = true })

		run(issuer(route, newStore()))
		data := placedCredential(grn)
		expectPair(data)

		requests := route.requests()
		Expect(len(requests)).To(BeNumerically(">=", 2), "the lost answer is asked for again")
		Expect(requests[1]).To(Equal(requests[0]), "the retry is identical")
		route.set(func(r *routeDouble) { Expect(r.issued).To(Equal(1), "one certificate is issued") })
		// The control: the placed certificate is the one the first request was
		// answered with.
		route.set(func(r *routeDouble) {
			Expect(string(data[garam.CertificateKey])).To(Equal(r.answered[requests[0].requestID].CertificatePEM))
		})
	})

	It("keeps the key on a superseded epoch, waits for the feed's next one, and sends it as a new request", func() {
		grn := "grn:acme:default:agent:3333333333333333"
		managedAgent(grn)
		route := serveRoute("8")

		run(issuer(route, newStore()))
		Eventually(func() int { return len(route.requests()) }, 10*time.Second).Should(Equal(1))
		By("asking nothing more while the agent's epoch is the superseded one")
		Consistently(func() int { return len(route.requests()) }, 3*time.Second).Should(Equal(1))
		_, placed := secret(agentname.CredentialsSecret(grn))
		Expect(placed).To(BeFalse())

		By("the feed rendering the next epoch")
		current := &agentv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(grn)}, current)).To(Succeed())
		moved := current.DeepCopy()
		moved.Spec.Identity.AssignmentEpoch = "8"
		Expect(k8sClient.Patch(ctx, moved, client.MergeFrom(current))).To(Succeed())

		data := placedCredential(grn)
		expectPair(data)
		requests := route.requests()
		Expect(requests).To(HaveLen(2))
		Expect(requests[1].epoch).To(Equal("8"))
		Expect(requests[1].csr).To(Equal(requests[0].csr), "the key and CSR are kept")
		Expect(requests[1].requestID).NotTo(Equal(requests[0].requestID), "a new epoch is a new request")
	})

	It("asks again soon after a 503 and only after the long wait after a 403", func() {
		By("the control: a 503 is asked again after the short wait, and then placed")
		transient := "grn:acme:default:agent:4444444444444444"
		managedAgent(transient)
		unavailable := serveRoute("7")
		unavailable.set(func(r *routeDouble) { r.refuse = http.StatusServiceUnavailable })
		stopTransient := run(issuer(unavailable, newStore()))
		Eventually(func() int { return len(unavailable.requests()) }, 10*time.Second).Should(Equal(1))
		unavailable.set(func(r *routeDouble) { r.refuse = 0 })
		expectPair(placedCredential(transient))
		// An issuer serves every managed agent in the namespace, so this one
		// stops before the next agent exists.
		stopTransient()

		By("a 403 is not asked again within the long wait")
		refused := "grn:acme:default:agent:5555555555555555"
		managedAgent(refused)
		forbidding := serveRoute("7")
		forbidding.set(func(r *routeDouble) { r.refuse = http.StatusForbidden })
		run(issuer(forbidding, newStore()))
		Eventually(func() int { return len(forbidding.requests()) }, 10*time.Second).Should(Equal(1))
		Consistently(func() int { return len(forbidding.requests()) }, 3*time.Second).Should(Equal(1))
		_, placed := secret(agentname.CredentialsSecret(refused))
		Expect(placed).To(BeFalse())
	})

	It("removes a request a placement left behind between its two writes, and asks for nothing", func() {
		grn := "grn:acme:default:agent:7777777777777777"
		managedAgent(grn)
		store := newStore()
		Expect(store.SaveRequest(ctx, grn, desired.PendingRequest{
			KeyPEM: []byte("key"), CSRPEM: []byte("csr"), Epoch: "7", ID: "id",
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: agentname.CredentialsSecret(grn), Namespace: namespace}})).To(Succeed())

		route := serveRoute("7")
		run(issuer(route, store))
		Eventually(func() bool { _, found := secret(agentname.CredentialRequestSecret(grn)); return found }, 10*time.Second).
			Should(BeFalse())
		Expect(route.requests()).To(BeEmpty())
		// The control: the credential placed by hand is left as it is.
		data, placed := secret(agentname.CredentialsSecret(grn))
		Expect(placed).To(BeTrue())
		Expect(data).To(BeEmpty())
	})

	It("places the credential whole, which the workload then delivers as ADR 0010 requires", func() {
		grn := "grn:acme:default:agent:6666666666666666"
		managedAgent(grn)
		reconciler := &controller.AgentReconciler{
			Client: k8sClient, Scheme: scheme.Scheme, APIReader: k8sClient, CopyImage: "example.com/copy:v1",
		}
		request := reconcile.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: agentname.Agent(grn)}}

		By("the control: no workload while only the request is persisted")
		store := newStore()
		Expect(store.SaveRequest(ctx, grn, desired.PendingRequest{
			KeyPEM: []byte("key"), CSRPEM: []byte("csr"), Epoch: "7", ID: "id",
		})).To(Succeed())
		_, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, request.NamespacedName, &appsv1.StatefulSet{})).
			To(MatchError(apierrors.IsNotFound, "a not-found error"))
		agent := &agentv1alpha1.Agent{}
		Expect(k8sClient.Get(ctx, request.NamespacedName, agent)).To(Succeed())
		Expect(meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionSynced).Reason).
			To(Equal(agentv1alpha1.ReasonCredentialsSecretMissing))
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: agentname.CredentialRequestSecret(grn), Namespace: namespace}})).To(Succeed())

		By("issuing, which places the four parts and nothing else")
		route := serveRoute("7")
		run(issuer(route, store))
		data := placedCredential(grn)
		Expect(data).To(HaveLen(4))
		Expect(data).To(HaveKey(garam.CertificateKey))
		Expect(data).To(HaveKey(garam.IssuerKey))
		Expect(data).To(HaveKey(garam.ServerRootKey))
		expectPair(data)
		block, _ := pem.Decode(data[garam.KeyKey])
		Expect(block.Type).To(Equal("PRIVATE KEY"))
		placed := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.CredentialsSecret(grn)}, placed)).To(Succeed())
		Expect(placed.OwnerReferences).To(ConsistOf(HaveField("Name", agentname.Agent(grn))))

		By("the workload copying it at mode 0600 into memory, for the one user every container runs as")
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		workload := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, request.NamespacedName, workload)).To(Succeed())
		pod := workload.Spec.Template.Spec
		Expect(pod.SecurityContext.RunAsUser).To(HaveValue(BeEquivalentTo(65532)))
		Expect(pod.SecurityContext.FSGroup).To(HaveValue(BeEquivalentTo(65532)))
		Expect(strings.Join(pod.InitContainers[0].Command, " ")).To(ContainSubstring("install -m 0600"))
		var copied *corev1.Volume
		for i := range pod.Volumes {
			if pod.Volumes[i].EmptyDir != nil && pod.Volumes[i].EmptyDir.Medium == corev1.StorageMediumMemory {
				copied = &pod.Volumes[i]
			}
		}
		Expect(copied).NotTo(BeNil(), "the copy is memory-backed")
	})
})
