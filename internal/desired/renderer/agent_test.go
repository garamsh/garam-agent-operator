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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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

const (
	embeddingBaseURL = "https://embeddings.example/v1"
	embeddingModel   = "bge-base-en-v1.5"
	modelKey         = "api-key"
	modelKeySecret   = "minimax"
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
				Embedding: &desired.Embedding{
					BaseURL: embeddingBaseURL, Name: embeddingModel, APIKeyRef: "minimax/api-key",
				},
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
			APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: modelKeySecret, Key: modelKey},
			Embedding: &agentv1alpha1.EmbeddingSpec{
				BaseURL: embeddingBaseURL, Name: embeddingModel,
				APIKeySecretRef: &agentv1alpha1.SecretKeyReference{Name: modelKeySecret, Key: modelKey},
			}}))
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

		// The last four fail only Kubernetes' own rules for a Secret's name and
		// data key, which a split on "/" alone would accept.
		for _, reference := range []string{"minimax", "minimax/", "/api-key", "a/b/c",
			"MiniMax/api-key", "minimax-/api-key", "minimax/api key", "minimax/.."} {
			malformed := revision(grn, "2", "7", map[string]string{requiredTool: secondPin}, "dropped")
			malformed.Configuration.Model.APIKeyRef = reference
			Expect(rendering.Render(ctx, malformed)).To(MatchError(desired.ErrMalformed), "reference %q", reference)
		}
		Expect(agentFor(grn).Spec).To(Equal(before.Spec))
	})

	It("leaves a model with no embedding unrendered unless it is the mock, beside one carrying it", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		grn := "grn:acme:default:agent:5656565656565656"
		Expect(rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, "kept"))).
			To(Succeed())
		before := agentFor(grn)
		Expect(before.Spec.Model.Embedding).NotTo(BeNil())

		missing := revision(grn, "2", "7", map[string]string{requiredTool: secondPin}, "dropped")
		missing.Configuration.Model.Embedding = nil
		Expect(rendering.Render(ctx, missing)).To(MatchError(desired.ErrMalformed))
		for _, embedding := range []desired.Embedding{
			{Name: embeddingModel}, {BaseURL: embeddingBaseURL},
			{BaseURL: embeddingBaseURL, Name: embeddingModel, APIKeyRef: "minimax"},
		} {
			malformed := revision(grn, "2", "7", map[string]string{requiredTool: secondPin}, "dropped")
			malformed.Configuration.Model.Embedding = &embedding
			Expect(rendering.Render(ctx, malformed)).To(MatchError(desired.ErrMalformed), "embedding %+v", embedding)
		}
		Expect(agentFor(grn).Spec).To(Equal(before.Spec))

		By("the mock, which runs with no embeddings endpoint")
		mock := "grn:acme:default:agent:5757575757575757"
		offline := revision(mock, "1", "7", map[string]string{requiredTool: firstPin}, "")
		offline.Configuration.Model.Provider, offline.Configuration.Model.Embedding = "mock", nil
		Expect(rendering.Render(ctx, offline)).To(Succeed())
		Expect(agentFor(mock).Spec.Model.Embedding).To(BeNil())
		Expect(agentFor(mock).Spec.Model.Provider).To(Equal("mock"))
	})

	It("refuses a later revision changing or dropping the embedding, beside one changing only its key", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		grn := "grn:acme:default:agent:5858585858585858"
		Expect(rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, ""))).
			To(Succeed())

		rekeyed := revision(grn, "2", "7", map[string]string{requiredTool: firstPin}, "")
		rekeyed.Configuration.Model.Embedding.APIKeyRef = "embeddings/key"
		Expect(rendering.Render(ctx, rekeyed)).To(Succeed())
		Expect(agentFor(grn).Spec.Model.Embedding.APIKeySecretRef).
			To(Equal(&agentv1alpha1.SecretKeyReference{Name: "embeddings", Key: "key"}))
		before := agentFor(grn)

		for _, change := range []func(*desired.Embedding){
			func(e *desired.Embedding) { e.Name = "text-embedding-3-small" },
			func(e *desired.Embedding) { e.BaseURL = "https://other.example/v1" },
			nil, // the model moved to the mock, dropping its embedding
		} {
			changed := revision(grn, "3", "7", map[string]string{requiredTool: firstPin}, "")
			changed.Configuration.Model.Embedding.APIKeyRef = "embeddings/key"
			if change == nil {
				changed.Configuration.Model.Provider, changed.Configuration.Model.Embedding = "mock", nil
			} else {
				change(changed.Configuration.Model.Embedding)
			}
			err := rendering.Render(ctx, changed)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot be changed or removed once set"))
		}
		Expect(agentFor(grn).Spec).To(Equal(before.Spec))
	})

	It("renders the profile's workspace size, clears it where the profile names none, and leaves a malformed one unrendered", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		grn := "grn:acme:default:agent:8a8a8a8a8a8a8a8a"
		sized := revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, "")
		sized.Profile.WorkspaceStorageSize = ptr.To("5Gi")
		Expect(rendering.Render(ctx, sized)).To(Succeed())
		Expect(agentFor(grn).Spec.WorkspaceStorageSize).To(Equal(ptr.To(resource.MustParse("5Gi"))))

		for _, size := range []string{"five", "0", "-1Gi"} {
			malformed := revision(grn, "2", "7", map[string]string{requiredTool: firstPin}, "")
			malformed.Profile.WorkspaceStorageSize = ptr.To(size)
			Expect(rendering.Render(ctx, malformed)).To(MatchError(desired.ErrMalformed), "size %q", size)
		}
		Expect(agentFor(grn).Spec.WorkspaceStorageSize).To(Equal(ptr.To(resource.MustParse("5Gi"))))

		By("a later revision whose profile names none, which leaves the claim to the storage size")
		Expect(rendering.Render(ctx, revision(grn, "2", "7", map[string]string{requiredTool: firstPin}, ""))).
			To(Succeed())
		Expect(agentFor(grn).Spec.WorkspaceStorageSize).To(BeNil())
	})

	It("takes the workspace size from the feed's profile, beside an agent whose profile names none", func() {
		sized := "grn:acme:default:agent:8b8b8b8b8b8b8b8b"
		plain := "grn:acme:default:agent:8c8c8c8c8c8c8c8c"
		withSize := revision(sized, "1", "7", map[string]string{requiredTool: firstPin}, "")
		withSize.Profile.WorkspaceStorageSize = ptr.To("3Gi")
		runPuller(serveFeed(feedAnswer("1", withSize, revision(plain, "1", "7", map[string]string{requiredTool: firstPin}, ""))))

		Eventually(func(g Gomega) {
			agent := &agentv1alpha1.Agent{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(sized)}, agent)).To(Succeed())
			g.Expect(agent.Spec.WorkspaceStorageSize).To(Equal(ptr.To(resource.MustParse("3Gi"))))
		}).Should(Succeed())
		Eventually(func(g Gomega) {
			agent := &agentv1alpha1.Agent{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(plain)}, agent)).To(Succeed())
			g.Expect(agent.Spec.WorkspaceStorageSize).To(BeNil())
		}).Should(Succeed())
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
		// The embedding arrived over the wire as configuration.model.embedding.
		Expect(left.Spec.Model.Embedding).To(Equal(&agentv1alpha1.EmbeddingSpec{
			BaseURL: embeddingBaseURL, Name: embeddingModel,
			APIKeySecretRef: &agentv1alpha1.SecretKeyReference{Name: modelKeySecret, Key: modelKey},
		}))
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
	type embedding struct {
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		APIKeyRef string `json:"apiKeyRef"`
	}
	type model struct {
		Provider  string     `json:"provider"`
		BaseURL   string     `json:"baseUrl"`
		Name      string     `json:"name"`
		APIKeyRef string     `json:"apiKeyRef"`
		Embedding *embedding `json:"embedding,omitempty"`
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

			WorkspaceStorageSize *string `json:"workspaceStorageSize,omitempty"`
		} `json:"profile"`
		Configuration struct {
			Model model             `json:"model"`
			Ego   string            `json:"ego"`
			Tools map[string]string `json:"tools"`
		} `json:"configuration"`
		Origin string `json:"origin,omitempty"`
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
		w.Profile.WorkspaceStorageSize = agent.Profile.WorkspaceStorageSize
		m := agent.Configuration.Model
		w.Configuration.Model = model{Provider: m.Provider, BaseURL: m.BaseURL, Name: m.Name, APIKeyRef: m.APIKeyRef}
		if m.Embedding != nil {
			w.Configuration.Model.Embedding = (*embedding)(m.Embedding)
		}
		w.Configuration.Ego, w.Configuration.Tools = agent.Configuration.Ego, agent.Configuration.Tools
		w.Origin = agent.Origin
		out.Agents = append(out.Agents, w)
	}
	raw, err := json.Marshal(out)
	Expect(err).NotTo(HaveOccurred())

	return string(raw)
}

// ownersOf is every field manager the Agent's managedFields record as owning
// the field at path, each step written as the API server writes it ("f:spec").
func ownersOf(agent *agentv1alpha1.Agent, path ...string) []string {
	GinkgoHelper()

	var owners []string
	for _, entry := range agent.ManagedFields {
		if entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		Expect(json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields)).To(Succeed())
		owned := true
		for _, step := range path {
			next, ok := fields[step].(map[string]any)
			if !ok {
				owned = false
				break
			}
			fields = next
		}
		if owned {
			owners = append(owners, entry.Manager)
		}
	}

	return owners
}

var _ = Describe("Renderer and a suspended agent", func() {
	It("leaves spec.suspended to the person who set it, while it renders every field it owns", func() {
		grn := "grn:acme:default:agent:5555555555555555"
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		Expect(rendering.Render(ctx, revision(grn, "1", "7", map[string]string{requiredTool: firstPin}, "first"))).
			To(Succeed())

		By("a person suspending the agent")
		created := agentFor(grn)
		suspended := created.DeepCopy()
		suspended.Spec.Suspended = true
		Expect(k8sClient.Patch(ctx, suspended, client.MergeFrom(created), client.FieldOwner("kubectl-edit"))).To(Succeed())

		By("a later revision, rendered while the agent is suspended")
		Expect(rendering.Render(ctx, revision(grn, "2", "8", map[string]string{otherTool: secondPin}, "second"))).
			To(Succeed())
		rendered := agentFor(grn)
		Expect(rendered.Spec.Ego).To(Equal("second"))
		Expect(rendered.Spec.Suspended).To(BeTrue(), "rendering cleared spec.suspended")

		By("the control: the renderer is recorded as the owner of what it renders")
		Expect(ownersOf(rendered, "f:spec", "f:ego")).To(ConsistOf("garam-operator-renderer"))
		Expect(ownersOf(rendered, "f:spec", "f:suspended")).To(ConsistOf("kubectl-edit"))
	})
})

// agentNamed creates an Agent under the name grn derives, with identity as
// given: none, as a person writes one, or another GRN's.
func agentNamed(grn string, identity *agentv1alpha1.AgentIdentity) {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: agentname.Agent(grn), Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image: image, CredentialsSecretName: agentname.CredentialsSecret(grn),
			StorageSize: resource.MustParse("1Gi"), Identity: identity,
		},
	})).To(Succeed())
}

// cutoverOf is a revision the feed marks as an agent cut over from garam.
func cutoverOf(grn, number string, pins map[string]string, ego string) desired.Agent {
	cut := revision(grn, number, "7", pins, ego)
	cut.Origin = desired.OriginCutover

	return cut
}

var _ = Describe("Renderer and a cutover", func() {
	It("moves a garam-source Agent the feed marks as cut over to the control source and renders it, beside one it does not mark", func() {
		cut := "grn:acme:default:agent:8888888888888881"
		absent := "grn:acme:default:agent:8888888888888882"
		unmarked := "grn:acme:default:agent:8888888888888883"
		before := writtenAgent(cut, agentv1alpha1.DesiredSourceGaram)
		writtenAgent(absent, "")
		untouched := writtenAgent(unmarked, agentv1alpha1.DesiredSourceGaram)
		feed := serveFeed(feedAnswer("1",
			cutoverOf(cut, "1", map[string]string{requiredTool: firstPin}, "imported"),
			cutoverOf(absent, "1", map[string]string{requiredTool: firstPin}, "imported"),
			revision(unmarked, "1", "7", map[string]string{requiredTool: firstPin}, "unmarked")))
		runPuller(feed)

		for _, grn := range []string{cut, absent} {
			Eventually(func(g Gomega) {
				agent := &agentv1alpha1.Agent{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentname.Agent(grn)}, agent)).
					To(Succeed())
				g.Expect(agent.Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceControl))
				g.Expect(agent.Spec.Ego).To(Equal("imported"))
				g.Expect(agent.Spec.Tools.Pins).To(Equal(map[string]string{requiredTool: firstPin}))
				g.Expect(agent.Spec.Identity.AssignmentEpoch).To(Equal("7"))
			}).Should(Succeed(), grn)
		}
		moved := agentFor(cut)
		Expect(moved.UID).To(Equal(before.UID))
		Expect(moved.Spec.CredentialsSecretName).To(Equal(before.Spec.CredentialsSecretName))
		Expect(ownersOf(moved, "f:spec", "f:identity", "f:source")).To(ContainElement("garam-operator-renderer"))

		By("the control: a garam-source Agent the feed names without the mark is refused, as before")
		Consistently(func() agentv1alpha1.AgentSpec { return agentFor(unmarked).Spec }, 2*time.Second).
			Should(Equal(untouched.Spec))
	})

	It("moves no Agent written by hand or naming another GRN, and refuses an origin nobody defined", func() {
		rendering := renderer.NewAgent(k8sClient, namespace, image)

		By("the control: a garam-source Agent of the same GRN moves")
		moves := "grn:acme:default:agent:9999999999999991"
		writtenAgent(moves, agentv1alpha1.DesiredSourceGaram)
		Expect(rendering.Render(ctx, cutoverOf(moves, "1", map[string]string{requiredTool: firstPin}, ""))).To(Succeed())
		Expect(agentFor(moves).Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceControl))

		By("an Agent written by hand, with no identity, under the GRN's name")
		handWritten := "grn:acme:default:agent:9999999999999992"
		agentNamed(handWritten, nil)
		Expect(rendering.Render(ctx, cutoverOf(handWritten, "1", map[string]string{requiredTool: firstPin}, ""))).
			To(MatchError(desired.ErrNotControlSource))
		Expect(agentFor(handWritten).Spec.Identity).To(BeNil())

		By("an Agent under the GRN's name whose identity names another GRN")
		other := "grn:acme:default:agent:9999999999999993"
		agentNamed(other, &agentv1alpha1.AgentIdentity{
			GRN: "grn:acme:default:agent:0000000000000000", Source: agentv1alpha1.DesiredSourceGaram})
		Expect(rendering.Render(ctx, cutoverOf(other, "1", map[string]string{requiredTool: firstPin}, ""))).
			To(MatchError(desired.ErrNotControlSource))
		Expect(agentFor(other).Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceGaram))

		By("an origin outside the closed set, which moves nothing")
		unknown := "grn:acme:default:agent:9999999999999994"
		writtenAgent(unknown, agentv1alpha1.DesiredSourceGaram)
		strange := cutoverOf(unknown, "1", map[string]string{requiredTool: firstPin}, "")
		strange.Origin = "migrated"
		Expect(rendering.Render(ctx, strange)).To(MatchError(desired.ErrMalformed))
		Expect(agentFor(unknown).Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceGaram))
	})

	It("writes nothing for the same cutover revision again, and renders a later one as any control-source revision", func() {
		grn := "grn:acme:default:agent:aaaaaaaaaaaaaaa1"
		rendering := renderer.NewAgent(k8sClient, namespace, image)
		writtenAgent(grn, agentv1alpha1.DesiredSourceGaram)
		Expect(rendering.Render(ctx, cutoverOf(grn, "1", map[string]string{requiredTool: firstPin}, "imported"))).To(Succeed())
		moved := agentFor(grn)

		By("the same cutover revision again")
		Expect(rendering.Render(ctx, cutoverOf(grn, "1", map[string]string{requiredTool: firstPin}, "imported"))).To(Succeed())
		Expect(agentFor(grn).ResourceVersion).To(Equal(moved.ResourceVersion))

		By("the control: a later revision, still marked, is rendered")
		Expect(rendering.Render(ctx, cutoverOf(grn, "2", map[string]string{requiredTool: secondPin}, "second"))).To(Succeed())
		later := agentFor(grn)
		Expect(later.Spec.Ego).To(Equal("second"))
		Expect(later.Spec.Identity.Source).To(Equal(agentv1alpha1.DesiredSourceControl))
	})
})
