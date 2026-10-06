package placement_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/desired/placement"
)

// namespace is the spec's own: a registrar serves every managed agent in its
// namespace, so one spec's agents would otherwise be registered by the next.
var (
	namespace  string
	namespaces int
)

var _ = BeforeEach(func() {
	namespaces++
	namespace = fmt.Sprintf("placement-%d", namespaces)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
})

// wireBody is a placement as the route double received it, in the pinned wire's
// names (#218).
type wireBody struct {
	Agent       string        `json:"-"`
	Epoch       string        `json:"epoch"`
	PodUID      string        `json:"podUid"`
	PVCUID      string        `json:"pvcUid"`
	TokenSHA256 string        `json:"tokenSha256"`
	Previous    *wirePrevious `json:"previous"`
}

type wirePrevious struct {
	PodUID              string `json:"podUid"`
	WriterStoppedSHA256 string `json:"writerStoppedSha256"`
}

// routeDouble stands in for the control service's placement route as #218 pins
// it: a new body for an agent is 201 and the agent's current placement, the same
// body again is 200, and a body naming a Pod the agent's current placement
// replaced is 409 placement_superseded. refuse answers every request with a
// status and kind instead.
type routeDouble struct {
	mu       sync.Mutex
	received []wireBody
	current  map[string]wireBody
	// created counts the bodies the route took as a new placement.
	created int
	refuse  int
	kind    string
	server  *httptest.Server
}

func (r *routeDouble) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	defer GinkgoRecover()
	var body wireBody
	Expect(json.NewDecoder(req.Body).Decode(&body)).To(Succeed())
	body.Agent = strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/operators/self/agents/"), "/placements")

	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, body)
	write := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	current, has := r.current[body.Agent]
	switch {
	case r.refuse != 0:
		write(r.refuse, map[string]string{"kind": r.kind, "message": "refused"})
	case has && current.PodUID == body.PodUID && samePrevious(current.Previous, body.Previous) &&
		current.Epoch == body.Epoch && current.PVCUID == body.PVCUID && current.TokenSHA256 == body.TokenSHA256:
		write(http.StatusOK, map[string]string{})
	default:
		r.current[body.Agent] = body
		r.created++
		write(http.StatusCreated, map[string]string{})
	}
}

func samePrevious(a, b *wirePrevious) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

func (r *routeDouble) creations() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.created
}

func (r *routeDouble) requests() []wireBody {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]wireBody(nil), r.received...)
}

func serveRoute() *routeDouble {
	route := &routeDouble{current: map[string]wireBody{}}
	route.server = httptest.NewTLSServer(route)
	DeferCleanup(route.server.Close)

	return route
}

// leaf is the fingerprint the registrar reads as the leaf it presents, which a
// spec changes to stand for a renewal.
type leaf struct {
	mu    sync.Mutex
	value string
}

func (l *leaf) read() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.value, nil
}

func (l *leaf) renew(value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.value = value
}

// run runs a registrar against route, reading placements in the spec's
// namespace, until the spec ends.
func run(route *routeDouble, presented *leaf) {
	DeferCleanup(start(route, presented))
}

// start starts a registrar against route, reading placements in the spec's
// namespace, and returns what stops it, as a manager's exit would: nothing it
// held in memory survives.
func start(route *routeDouble, presented *leaf) (stop func()) {
	roots := x509.NewCertPool()
	roots.AddCert(route.server.Certificate())
	registrar := desired.NewRegistrar(
		desired.NewClient(route.server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}),
		placement.NewPods(k8sClient, k8sClient, namespace), presented.read)

	runCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		_ = registrar.Start(runCtx)
		close(stopped)
	}()
	return func() {
		cancel()
		<-stopped
	}
}

// placed creates what the manager builds for grn: an Agent on source at epoch
// 7, a placement Secret holding token and annotated with previous, and the
// running Pod of its workload, carrying garam's adapter where adapter is set and
// the claim the writer fence recorded. It returns the Pod.
func placed(grn string, source agentv1alpha1.DesiredSource, adapter bool, token string,
	previous map[string]string) *corev1.Pod {
	GinkgoHelper()

	name := agentname.Agent(grn)
	Expect(k8sClient.Create(ctx, &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image: "example.com/sherlock:v1", CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize: resource.MustParse("1Gi"),
			Identity:    &agentv1alpha1.AgentIdentity{GRN: grn, AssignmentEpoch: "7", Source: source},
		},
	})).To(Succeed())
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.PlacementSecret(name), Namespace: namespace, Annotations: previous},
		Data:       map[string][]byte{agentname.PlacementTokenKey: []byte(token)},
	})).To(Succeed())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name + "-0", Namespace: namespace,
			Annotations: map[string]string{agentname.PVCUIDAnnotation: "pvc-of-" + name},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "example.com/sherlock:v1"}}},
	}
	if adapter {
		pod.Spec.InitContainers = []corev1.Container{{
			Name: agentname.AdapterContainer, Image: "example.com/garam:v1",
			RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		}}
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	pod.Status.Phase = corev1.PodRunning
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

	By("reading back the running Pod, which is what leaves the registrar something to register")
	read := &corev1.Pod{}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), read)).To(Succeed())
	Expect(read.Status.Phase).To(Equal(corev1.PodRunning))

	return read
}

// firstLeaf is the leaf each spec starts presenting.
const firstLeaf = "leaf-a"

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))

	return hex.EncodeToString(sum[:])
}

var _ = Describe("Placement registrar", func() {
	It("registers a managed agent's first placement with no previous, and no agent that does not register", func() {
		managed := "grn:acme:default:agent:1111111111111111"
		pod := placed(managed, agentv1alpha1.DesiredSourceControl, true, "token-one", nil)
		placed("grn:acme:default:agent:2222222222222222", agentv1alpha1.DesiredSourceGaram, true, "token-two", nil)
		placed("grn:acme:default:agent:3333333333333333", agentv1alpha1.DesiredSourceControl, false, "token-three", nil)
		route := serveRoute()
		run(route, &leaf{value: firstLeaf})

		Eventually(route.requests, 10*time.Second).Should(HaveLen(1))
		Consistently(route.requests, 2*time.Second).Should(Equal([]wireBody{{
			Agent: managed, Epoch: "7", PodUID: string(pod.UID), PVCUID: "pvc-of-" + agentname.Agent(managed),
			TokenSHA256: sha256Hex("token-one"), Previous: nil,
		}}))
	})

	It("names the placement a replacement replaces, with the digest of its evidence", func() {
		managed := "grn:acme:default:agent:4444444444444444"
		digest := sha256Hex("evidence")
		pod := placed(managed, agentv1alpha1.DesiredSourceControl, true, "token-next", map[string]string{
			agentname.PreviousPodUIDAnnotation:        "pod-before",
			agentname.PreviousWriterStoppedAnnotation: digest,
		})
		route := serveRoute()
		run(route, &leaf{value: firstLeaf})

		Eventually(route.requests, 10*time.Second).Should(HaveLen(1))
		sent := route.requests()[0]
		Expect(sent.PodUID).To(Equal(string(pod.UID)))
		Expect(sent.Previous).To(Equal(&wirePrevious{PodUID: "pod-before", WriterStoppedSHA256: digest}))
		Expect(sent.TokenSHA256).To(Equal(sha256Hex("token-next")))
	})

	It("registers a replacement once, with the evidence the fence recorded, across a restart after the fence", func() {
		// The window #212 names: the writer fence has recorded the evidence on the
		// placement Secret and removed its finalizer, and the replacing Pod runs.
		managed := "grn:acme:default:agent:4545454545454545"
		digest := sha256Hex("evidence")
		pod := placed(managed, agentv1alpha1.DesiredSourceControl, true, "token-next", map[string]string{
			agentname.PreviousPodUIDAnnotation:        "pod-before",
			agentname.PreviousWriterStoppedAnnotation: digest,
		})
		route := serveRoute()

		By("a first manager registering the replacement, then exiting")
		stop := start(route, &leaf{value: firstLeaf})
		Eventually(route.creations, 10*time.Second).Should(Equal(1))
		stop()
		before := len(route.requests())

		By("a restarted manager, which holds nothing of the first and reads the placement off its objects")
		run(route, &leaf{value: firstLeaf})
		Eventually(func() int { return len(route.requests()) }, 10*time.Second).Should(BeNumerically(">", before))
		Consistently(route.creations, 3*time.Second).Should(Equal(1), "the restart registered the placement twice")
		want := wireBody{
			Agent: managed, Epoch: "7", PodUID: string(pod.UID), PVCUID: "pvc-of-" + agentname.Agent(managed),
			TokenSHA256: sha256Hex("token-next"), Previous: &wirePrevious{PodUID: "pod-before", WriterStoppedSHA256: digest},
		}
		for _, sent := range route.requests() {
			Expect(sent).To(Equal(want))
		}

		By("the control: a Pod the replacement then gives way to is registered as a new placement")
		releaseAndDelete(pod)
		next := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: namespace, Annotations: pod.Annotations},
			Spec:       pod.Spec,
		}
		Expect(k8sClient.Create(ctx, next)).To(Succeed())
		next.Status.Phase = corev1.PodRunning
		Expect(k8sClient.Status().Update(ctx, next)).To(Succeed())
		Eventually(route.creations, 10*time.Second).Should(Equal(2))
	})

	It("presents a placement again, unchanged, only once the leaf it was accepted under is renewed", func() {
		managed := "grn:acme:default:agent:5555555555555555"
		placed(managed, agentv1alpha1.DesiredSourceControl, true, "token-one", nil)
		route := serveRoute()
		presented := &leaf{value: firstLeaf}
		run(route, presented)

		By("the control: under one leaf, a registered placement is not presented again")
		Eventually(route.requests, 10*time.Second).Should(HaveLen(1))
		Consistently(route.requests, 3*time.Second).Should(HaveLen(1))

		By("the leaf renewed, the same body presented once more, which the route answers as a refresh")
		presented.renew("leaf-b")
		Eventually(route.requests, 10*time.Second).Should(HaveLen(2))
		Consistently(route.requests, 3*time.Second).Should(HaveLen(2))
		requests := route.requests()
		Expect(requests[1]).To(Equal(requests[0]))
	})

	It("presents a superseded placement once and surfaces it, and presents the Pod that replaced it", func() {
		managed := "grn:acme:default:agent:6666666666666666"
		superseded := placed(managed, agentv1alpha1.DesiredSourceControl, true, "token-one", nil)
		route := serveRoute()
		route.mu.Lock()
		route.refuse, route.kind = http.StatusConflict, "placement_superseded"
		route.mu.Unlock()
		presented := &leaf{value: firstLeaf}
		run(route, presented)

		By("refused for good: presented once, and not again under a renewed leaf either")
		Eventually(route.requests, 10*time.Second).Should(HaveLen(1))
		presented.renew("leaf-b")
		Consistently(route.requests, 3*time.Second).Should(HaveLen(1))

		By("the control: the Pod replacing it is a new placement, which is presented and accepted")
		route.mu.Lock()
		route.refuse, route.kind = 0, ""
		route.mu.Unlock()
		releaseAndDelete(superseded)
		next := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: superseded.Name, Namespace: namespace, Annotations: superseded.Annotations},
			Spec:       superseded.Spec,
		}
		Expect(k8sClient.Create(ctx, next)).To(Succeed())
		next.Status.Phase = corev1.PodRunning
		Expect(k8sClient.Status().Update(ctx, next)).To(Succeed())
		Eventually(route.requests, 10*time.Second).Should(HaveLen(2))
		requests := route.requests()
		Expect(requests[0].PodUID).To(Equal(string(superseded.UID)))
		Expect(requests[1].PodUID).To(Equal(string(next.UID)))
	})
})

// releaseAndDelete deletes a Pod with no grace and waits until it is gone, as
// the fence's release would let it go.
func releaseAndDelete(pod *corev1.Pod) {
	GinkgoHelper()

	Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
	Eventually(func() bool {
		return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}))
	}, 10*time.Second).Should(BeTrue())
}
