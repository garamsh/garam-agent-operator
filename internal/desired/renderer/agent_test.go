package renderer_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/desired/renderer"
)

const (
	namespace = "default"
	image     = "example.com/sherlock:v1"

	// requiredTool and otherTool are tools a revision pins, and firstPin and
	// secondPin the pins the first and second revisions give them.
	requiredTool = "message_send"
	otherTool    = "files"
	firstPin     = "sha256:aa"
	secondPin    = "sha256:bb"
)

// revision is a revision of grn as the feed releases it, at epoch, with pins
// and an ego that differ from revision to revision.
func revision(grn, number, epoch string, pins map[string]string, ego string) desired.Agent {
	return desired.Agent{
		GRN: grn, Revision: number, Epoch: epoch,
		Profile: desired.Profile{
			Name: "standard", Version: 1, StorageSize: "1Gi",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
		},
		Configuration: desired.Configuration{
			Model: desired.Model{
				Provider: "openai-compatible", BaseURL: "https://api.minimax.io/v1", Name: "MiniMax-M2",
				APIKeyRef: "minimax/api-key",
			},
			Ego:   ego,
			Tools: pins,
		},
	}
}

// agentFor reads the Agent rendered for grn.
func agentFor(grn string) *agentv1alpha1.Agent {
	GinkgoHelper()

	agent := &agentv1alpha1.Agent{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(grn)}, agent)).To(Succeed())

	return agent
}

// writtenAgent creates the Agent a garam definition's construction, or a
// person, would have built for grn, with identity's source as given.
func writtenAgent(grn string, source agentv1alpha1.DesiredSource) *agentv1alpha1.Agent {
	GinkgoHelper()

	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(grn), Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 image,
			CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize:           resource.MustParse("1Gi"),
			Tools:                 agentv1alpha1.ToolSet{Pins: map[string]string{requiredTool: "sha256:garam"}},
			Identity:              &agentv1alpha1.AgentIdentity{GRN: grn, Source: source},
		},
	}
	Expect(k8sClient.Create(ctx, agent)).To(Succeed())

	return agent
}

var _ = Describe("Renderer", func() {
	It("creates the Agent of a new GRN on the control source, and renders every later revision into it", func() {
		grn := "grn:acme:default:agent:1111111111111111"
		rendering := renderer.NewAgent(k8sClient, namespace, image)

		Expect(rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, "first"))).
			To(Succeed())
		first := agentFor(grn)
		Expect(first.Spec.Identity).To(Equal(&agentv1alpha1.AgentIdentity{
			GRN: grn, AssignmentEpoch: "7", Source: agentv1alpha1.DesiredSourceControl}))
		Expect(first.Spec.CredentialsSecretName).To(Equal(agentname.CredentialsSecret(grn)))
		Expect(first.Spec.Image).To(Equal(image))
		Expect(first.Spec.StorageSize).To(Equal(resource.MustParse("1Gi")))
		Expect(first.Spec.Model).To(Equal(&agentv1alpha1.ModelSpec{
			Provider: "openai-compatible", BaseURL: "https://api.minimax.io/v1", Name: "MiniMax-M2",
			APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: "minimax", Key: "api-key"}}))
		Expect(first.Spec.Tools.Pins).To(Equal(map[string]string{requiredTool: firstPin}))
		Expect(first.Spec.Ego).To(Equal("first"))

		By("a later revision, which every rendered field follows")
		Expect(rendering.Render(ctx, revision(grn, "2", "8", map[string]string{otherTool: secondPin}, "second"))).
			To(Succeed())
		second := agentFor(grn)
		Expect(second.UID).To(Equal(first.UID))
		Expect(second.Spec.Tools.Pins).To(Equal(map[string]string{otherTool: secondPin}))
		Expect(second.Spec.Ego).To(Equal("second"))
		Expect(second.Spec.Identity.AssignmentEpoch).To(Equal("8"))

		By("the same revision again, which writes nothing")
		Expect(rendering.Render(ctx, revision(grn, "2", "8", map[string]string{otherTool: secondPin}, "second"))).
			To(Succeed())
		Expect(agentFor(grn).ResourceVersion).To(Equal(second.ResourceVersion))
	})

	It("never renders a GRN whose Agent is on the garam source, beside one on the control source", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)

		By("the control: an Agent the control source created takes a later revision")
		controlled := "grn:acme:default:agent:2222222222222222"
		Expect(rendering.Render(ctx, revision(controlled, "1", "7", map[string]string{requiredTool: firstPin}, ""))).
			To(Succeed())
		Expect(rendering.Render(ctx, revision(controlled, "2", "7", map[string]string{requiredTool: "sha256:cc"}, ""))).
			To(Succeed())
		Expect(agentFor(controlled).Spec.Tools.Pins).To(HaveKeyWithValue(requiredTool, "sha256:cc"))

		for _, source := range []agentv1alpha1.DesiredSource{agentv1alpha1.DesiredSourceGaram, ""} {
			grn := "grn:acme:default:agent:333333333333333" + map[agentv1alpha1.DesiredSource]string{
				agentv1alpha1.DesiredSourceGaram: "3", "": "4"}[source]
			before := writtenAgent(grn, source)

			err := rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: "sha256:control"}, "ego"))
			Expect(err).To(MatchError(desired.ErrNotControlSource), "source %q", source)
			after := agentFor(grn)
			Expect(after.Spec).To(Equal(before.Spec), "source %q", source)
		}
	})

	It("leaves a revision with a malformed key reference unrendered, beside a well-formed one", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		grn := "grn:acme:default:agent:5555555555555555"
		Expect(rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, "kept"))).
			To(Succeed())
		before := agentFor(grn)

		for _, reference := range []string{"minimax", "minimax/", "/api-key", "a/b/c"} {
			malformed := revision(grn, "2", "7", map[string]string{requiredTool: secondPin}, "dropped")
			malformed.Configuration.Model.APIKeyRef = reference
			Expect(rendering.Render(ctx, malformed)).To(MatchError(desired.ErrMalformed), "reference %q", reference)
		}
		Expect(agentFor(grn).Spec).To(Equal(before.Spec))
	})

	It("leaves an agent the feed withholds as it was, deletes nothing, and renders the rest", func() {
		withheld := "grn:acme:default:agent:6666666666666666"
		kept := "grn:acme:default:agent:7777777777777777"
		feed := serveFeed(
			feedAnswer("1", revision(withheld, "1", "7", map[string]string{requiredTool: firstPin}, "withheld"),
				revision(kept, "1", "7", map[string]string{requiredTool: firstPin}, "kept")),
			feedAnswer("2", revision(kept, "2", "7", map[string]string{requiredTool: secondPin}, "kept")),
		)
		runPuller(feed)

		Eventually(func(g Gomega) {
			agent := &agentv1alpha1.Agent{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(kept)}, agent)).To(Succeed())
			g.Expect(agent.Spec.Tools.Pins).To(HaveKeyWithValue(requiredTool, secondPin))
		}).Should(Succeed())

		left := agentFor(withheld)
		Expect(left.DeletionTimestamp).To(BeNil())
		Expect(left.Spec.Tools.Pins).To(HaveKeyWithValue(requiredTool, firstPin))
		Expect(left.Spec.Ego).To(Equal("withheld"))
	})
})

// feedServer stands in for the control service's controller routes: it answers
// the desired feed from a script, then waits as a long poll on a feed that never
// moves would, and accepts every status report.
type feedServer struct {
	mu     sync.Mutex
	script []string
	done   chan struct{}
	server *httptest.Server
}

func (f *feedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/status") {
		_, _ = w.Write([]byte(`{}`))

		return
	}
	f.mu.Lock()
	if len(f.script) == 0 {
		f.mu.Unlock()
		select {
		case <-f.done:
		case <-r.Context().Done():
		}

		return
	}
	body := f.script[0]
	f.script = f.script[1:]
	f.mu.Unlock()
	_, _ = w.Write([]byte(body))
}

// serveFeed starts a TLS feed double answering script, and stops it when the
// spec ends.
func serveFeed(script ...string) *feedServer {
	feed := &feedServer{script: script, done: make(chan struct{})}
	feed.server = httptest.NewTLSServer(feed)
	DeferCleanup(func() {
		close(feed.done)
		feed.server.Close()
	})

	return feed
}

// runPuller runs the real puller, client and renderer against feed until the
// spec ends.
func runPuller(feed *feedServer) {
	roots := x509.NewCertPool()
	roots.AddCert(feed.server.Certificate())
	feedClient := desired.NewClient(feed.server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	puller := desired.NewPuller(feedClient, renderer.NewAgent(k8sClient, namespace, image))

	pullCtx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer GinkgoRecover()
		_ = puller.Start(pullCtx)
		close(stopped)
	}()
	DeferCleanup(func() {
		stop()
		<-stopped
	})
}

// feedAnswer is the C2 wire's answer for a cursor and agents (ADR 0040).
func feedAnswer(cursor string, agents ...desired.Agent) string {
	type model struct {
		Provider  string `json:"provider"`
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		APIKeyRef string `json:"apiKeyRef"`
	}
	type wire struct {
		Agent    string `json:"agent"`
		Revision string `json:"revision"`
		Epoch    string `json:"epoch"`
		Profile  struct {
			Name        string                      `json:"name"`
			Version     int64                       `json:"version"`
			Resources   corev1.ResourceRequirements `json:"resources"`
			StorageSize string                      `json:"storageSize"`
		} `json:"profile"`
		Configuration struct {
			Model model             `json:"model"`
			Ego   string            `json:"ego"`
			Tools map[string]string `json:"tools"`
		} `json:"configuration"`
	}
	out := struct {
		Cursor string `json:"cursor"`
		Agents []wire `json:"agents"`
	}{Cursor: cursor}
	for _, agent := range agents {
		var w wire
		w.Agent, w.Revision, w.Epoch = agent.GRN, agent.Revision, agent.Epoch
		w.Profile.Name, w.Profile.Version = agent.Profile.Name, agent.Profile.Version
		w.Profile.Resources, w.Profile.StorageSize = agent.Profile.Resources, agent.Profile.StorageSize
		w.Configuration.Model = model(agent.Configuration.Model)
		w.Configuration.Ego, w.Configuration.Tools = agent.Configuration.Ego, agent.Configuration.Tools
		out.Agents = append(out.Agents, w)
	}
	raw, err := json.Marshal(out)
	Expect(err).NotTo(HaveOccurred())

	return string(raw)
}
