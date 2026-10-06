package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// The Agent CRDs #289's upgrade goes from and to: the one at 7c21646, before #278's embedding
// rule, and the one config/crd/bases holds now, which the suite installed.
var (
	crdBefore = filepath.Join("..", "..", "test", "testdata", "crd-7c21646", "agent.garam.sh_agents.yaml")
	crdNow    = filepath.Join("..", "..", "config", "crd", "bases", "agent.garam.sh_agents.yaml")
)

// crdClient writes CustomResourceDefinitions, which the suite's scheme does not hold.
func crdClient() client.Client {
	GinkgoHelper()
	crdScheme := runtime.NewScheme()
	Expect(apiextensionsv1.AddToScheme(crdScheme)).To(Succeed())
	Expect(agentv1alpha1.AddToScheme(crdScheme)).To(Succeed())
	c, err := client.New(cfg, client.Options{Scheme: crdScheme})
	Expect(err).NotTo(HaveOccurred())
	return c
}

// swapAgentCRD replaces the installed Agent CRD with the one at path, and returns once the API
// server serves it: an embedding written on a mock model is kept under a CRD that knows the field
// and pruned under one that does not.
func swapAgentCRD(c client.Client, path string, knowsEmbedding bool) {
	GinkgoHelper()
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	next := &apiextensionsv1.CustomResourceDefinition{}
	Expect(yaml.Unmarshal(raw, next)).To(Succeed())
	installed := &apiextensionsv1.CustomResourceDefinition{}
	Expect(c.Get(ctx, client.ObjectKey{Name: next.Name}, installed)).To(Succeed())
	installed.Spec = next.Spec
	Expect(c.Update(ctx, installed)).To(Succeed())

	probes := 0
	Eventually(func(g Gomega) {
		probes++
		probe := &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("crd-probe-%d", probes), Namespace: agentNamespace},
			Spec: agentv1alpha1.AgentSpec{Image: "x", CredentialsSecretName: "x", StorageSize: resource.MustParse("1Gi"),
				Model: &agentv1alpha1.ModelSpec{Provider: "mock", BaseURL: "x", Name: "x",
					APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: "x", Key: "x"},
					Embedding:       &agentv1alpha1.EmbeddingSpec{BaseURL: "x", Name: "x"}}},
		}
		g.Expect(c.Create(ctx, probe)).To(Succeed())
		read := &agentv1alpha1.Agent{}
		err := c.Get(ctx, client.ObjectKeyFromObject(probe), read)
		_ = c.Delete(ctx, probe)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(read.Spec.Model.Embedding != nil).To(Equal(knowsEmbedding))
	}, 30*time.Second, 200*time.Millisecond).Should(Succeed(), "the API server never served %s", path)
}

var _ = Describe("Agent CRD upgrade", func() {
	// #289 (c): the reconciler's first write to an Agent, the workload fence, is accepted for an
	// Agent stored before #278's rule with a model other than mock and no embedding, because the
	// API server ratchets the rule. The spec correction the constructor writes is asserted in
	// internal/garam/constructor.
	It("fences an Agent stored before the embedding rule, once the CRD carries the rule", func() {
		c := crdClient()
		DeferCleanup(func() { swapAgentCRD(c, crdNow, true) })
		swapAgentCRD(c, crdBefore, false)

		name := "crd-upgrade-stored"
		Expect(k8sClient.Create(ctx, &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agentNamespace},
			Spec: agentv1alpha1.AgentSpec{
				Image: "example.com/sherlock:837b80a", CredentialsSecretName: name + "-credentials",
				StorageSize: resource.MustParse("1Gi"),
				Model: &agentv1alpha1.ModelSpec{
					Provider: "openai-compatible", BaseURL: "https://api.minimax.io/v1", Name: "MiniMax-M2",
					APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: "minimax", Key: "api-key"},
				},
			},
		})).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Patch(ctx, readAgent(name),
				client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`)))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, readAgent(name)))).To(Succeed())
		})
		Expect(readAgent(name).Spec.Model.Embedding).To(BeNil())

		swapAgentCRD(c, crdNow, true)
		_, err := reconcileAgent(name)
		Expect(readAgent(name).Finalizers).To(ContainElement(workloadFencedFinalizer),
			"the reconciler's workload fence was refused: %v", err)
	})

	// #291: an Agent stored before spec.revision existed takes one once the CRD carries it, as the
	// renderer's merge patch writes it, and the config file carries it. A Garam-source one is still
	// written as before, and is refused a revision.
	It("renders a revision into a Control-source Agent stored before the field, and refuses one on a Garam-source Agent", func() {
		c := crdClient()
		DeferCleanup(func() { swapAgentCRD(c, crdNow, true) })
		swapAgentCRD(c, crdBefore, false)

		stored := func(name string, source agentv1alpha1.DesiredSource) {
			GinkgoHelper()
			createSecret(credentialsSecretName(name))
			agent := newAgent(name)
			agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, Source: source}
			createAgent(agent)
			Expect(readAgent(name).Spec.Revision).To(BeEmpty())
		}
		control, garamSource := "crd-upgrade-control", "crd-upgrade-garam"
		stored(control, agentv1alpha1.DesiredSourceControl)
		stored(garamSource, agentv1alpha1.DesiredSourceGaram)

		swapAgentCRD(c, crdNow, true)
		Expect(k8sClient.Patch(ctx, readAgent(control),
			client.RawPatch(types.MergePatchType, []byte(`{"spec":{"revision":"2"}}`)))).To(Succeed())
		Expect(readAgent(control).Spec.Revision).To(Equal("2"))
		_, err := reconcileAgent(control)
		Expect(err).NotTo(HaveOccurred())
		Expect(environmentOf(initContainerOf(statefulSetFor(control).Spec.Template.Spec, configContainerName))).
			To(HaveKeyWithValue(configContentVariable, "revision: \"2\"\n"))

		By("the Garam-source Agent, which is written as before and refused a revision")
		Expect(k8sClient.Patch(ctx, readAgent(garamSource),
			client.RawPatch(types.MergePatchType, []byte(`{"spec":{"suspended":true}}`)))).To(Succeed())
		Expect(k8sClient.Patch(ctx, readAgent(garamSource),
			client.RawPatch(types.MergePatchType, []byte(`{"spec":{"revision":"2"}}`)))).
			To(MatchError(ContainSubstring("revision is set only on an agent whose identity.source is Control")))
	})
})
