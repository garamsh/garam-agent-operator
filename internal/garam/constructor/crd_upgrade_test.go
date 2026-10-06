package constructor_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/controller"
	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/garam/constructor"
)

// The CRDs the upgrade goes from and to: the one at 7c21646, before #278's embedding rule, and the
// one config/crd/bases holds now.
const (
	crdBefore = "testdata/agent-crd-7c21646.yaml"
	crdNow    = "../../../config/crd/bases/agent.garam.sh_agents.yaml"
)

// embeddingRequired is the message #278's rule refuses a model with no embedding under.
const embeddingRequired = "embedding is required"

// workloadFenced is the finalizer the Agent reconciler writes onto an Agent before it builds
// anything for it.
const workloadFenced = "agent.garam.sh/workload-fenced"

// upgradeEnv is an API server holding the Agent CRD from before #278, and a client of it.
type upgradeEnv struct {
	client    client.Client
	scheme    *runtime.Scheme
	namespace string
}

func newUpgradeEnv(t *testing.T) *upgradeEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset: run through make test, which sets it")
	}
	env := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{Paths: []string{crdBefore}}, ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, agentv1alpha1.AddToScheme(scheme))
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	e := &upgradeEnv{client: c, scheme: scheme, namespace: "lab"}
	require.NoError(t, c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.namespace}}))
	return e
}

// store creates the Agent of grn as a Garam-source agent the poller constructed and reported,
// with model, which may be nil.
func (e *upgradeEnv) store(t *testing.T, grn garam.GRN, model *agentv1alpha1.ModelSpec) *agentv1alpha1.Agent {
	t.Helper()
	ctx := context.Background()
	agent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: constructor.Name(grn), Namespace: e.namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image: "example.com/sherlock:837b80a", CredentialsSecretName: constructor.Name(grn) + "-credentials",
			StorageSize: resource.MustParse("1Gi"), Model: model,
			Identity: &agentv1alpha1.AgentIdentity{GRN: string(grn), AssignmentEpoch: "1",
				Source: agentv1alpha1.DesiredSourceGaram},
		},
	}
	require.NoError(t, e.client.Create(ctx, agent))
	agent.Status.Agent, agent.Status.Epoch = string(grn), 1
	require.NoError(t, e.client.Status().Update(ctx, agent))
	return agent
}

// upgrade replaces the installed Agent CRD with the one at path, and returns once the API server
// serves the new schema: an embedding written on a mock model is kept rather than pruned as a
// field the old schema does not know. Every write after it meets the new schema.
func (e *upgradeEnv) upgrade(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	next := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal(raw, next))
	installed := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, e.client.Get(ctx, client.ObjectKey{Name: next.Name}, installed))
	installed.Spec = next.Spec
	require.NoError(t, e.client.Update(ctx, installed))

	probes := 0
	require.Eventually(t, func() bool {
		probes++
		model := modelWithoutEmbedding("mock")
		model.Provider = "mock"
		model.Embedding = &agentv1alpha1.EmbeddingSpec{BaseURL: "https://embeddings.example/v1", Name: "bge"}
		agent := &agentv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("probe-%d", probes), Namespace: e.namespace},
			Spec: agentv1alpha1.AgentSpec{Image: "x", CredentialsSecretName: "x", StorageSize: resource.MustParse("1Gi"),
				Model: model},
		}
		if err := e.client.Create(ctx, agent); err != nil {
			return false
		}
		read := &agentv1alpha1.Agent{}
		err := e.client.Get(ctx, client.ObjectKeyFromObject(agent), read)
		_ = e.client.Delete(ctx, agent)
		return err == nil && read.Spec.Model != nil && read.Spec.Model.Embedding != nil
	}, 30*time.Second, 200*time.Millisecond, "the API server never served the new CRD's schema")
}

// modelWithoutEmbedding is a model other than mock naming no embedding, as the lab's agents were
// written before #278.
func modelWithoutEmbedding(name string) *agentv1alpha1.ModelSpec {
	return &agentv1alpha1.ModelSpec{
		Provider: "openai-compatible", BaseURL: "https://api.minimax.io/v1", Name: name,
		APIKeySecretRef: agentv1alpha1.SecretKeyReference{Name: "minimax", Key: "key"},
	}
}

// patch merge-patches agent with spec, as kubectl patch --type merge and gitops do.
func (e *upgradeEnv) patch(agent *agentv1alpha1.Agent, spec string) error {
	return e.client.Patch(context.Background(), agent.DeepCopy(),
		client.RawPatch(types.MergePatchType, []byte(`{"spec":`+spec+`}`)))
}

// TestCRDUpgrade_AnAgentStoredBeforeTheEmbeddingRuleIsStillWrittenAsTheRolloutWrites is #289: an
// Agent stored under the CRD from before #278, with a model other than mock and no embedding, is
// still written by the rollout's own writes once the CRD carries #278's rule, through the
// API server's validation ratcheting, and a change to its model is refused.
func TestCRDUpgrade_AnAgentStoredBeforeTheEmbeddingRuleIsStillWrittenAsTheRolloutWrites(t *testing.T) {
	ctx := context.Background()
	e := newUpgradeEnv(t)
	suspended := e.store(t, "grn:lab:default:agent:a", modelWithoutEmbedding("MiniMax-M2"))
	edited := e.store(t, "grn:lab:default:agent:b", modelWithoutEmbedding("MiniMax-M2"))
	corrected := e.store(t, "grn:lab:default:agent:c", modelWithoutEmbedding("MiniMax-M2"))
	reconciled := e.store(t, "grn:lab:default:agent:d", modelWithoutEmbedding("MiniMax-M2"))
	remodelled := e.store(t, "grn:lab:default:agent:e", modelWithoutEmbedding("MiniMax-M2"))
	modelless := e.store(t, "grn:lab:default:agent:f", nil)

	e.upgrade(t, crdNow)

	// (a) ADR 0047's step 2.
	// (a)-(c) are each asserted without stopping, so a refusal of one is reported beside the others.
	assert.NoError(t, e.patch(suspended, `{"suspended":true}`), "suspending the agent was refused")
	read := &agentv1alpha1.Agent{}
	require.NoError(t, e.client.Get(ctx, client.ObjectKeyFromObject(suspended), read))
	assert.True(t, read.Spec.Suspended)

	// (b) An edit elsewhere in the spec.
	assert.NoError(t, e.patch(edited, `{"ego":"edited after the upgrade"}`), "an unrelated spec edit was refused")

	// (c) The manager's own writes of a Garam-source agent: the spec correction to the image the
	// manager now runs (ADR 0018), and the reconciler's first write, the workload fence.
	correcting := constructor.NewAgent(e.client, e.scheme, e.namespace, "example.com/sherlock:next",
		resource.MustParse("1Gi"), nil)
	changed, err := correcting.CorrectSpec(ctx, "grn:lab:default:agent:c")
	assert.NoError(t, err, "the manager's spec correction was refused")
	assert.True(t, changed)
	require.NoError(t, e.client.Get(ctx, client.ObjectKeyFromObject(corrected), read))
	assert.Equal(t, "example.com/sherlock:next", read.Spec.Image)

	reconciler := &controller.AgentReconciler{Client: e.client, Scheme: e.scheme, APIReader: e.client,
		CopyImage: "example.com/copy:v1"}
	_, reconcileErr := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(reconciled)})
	require.NoError(t, e.client.Get(ctx, client.ObjectKeyFromObject(reconciled), read))
	assert.True(t, controllerutil.ContainsFinalizer(read, workloadFenced),
		"the reconciler's workload fence was refused: %v", reconcileErr)

	// An Agent with no model at all, as the lab's is, has nothing the rule reads.
	assert.NoError(t, e.patch(modelless, `{"suspended":true}`), "suspending an agent with no model was refused")
	// Control: the rule is in force once that Agent is given a model without an embedding.
	err = e.patch(modelless, `{"model":{"provider":"openai-compatible","baseURL":"https://api.minimax.io/v1",`+
		`"name":"MiniMax-M2","apiKeySecretRef":{"name":"minimax","key":"key"}}}`)
	if assert.Error(t, err, "a model without an embedding was added to an agent with none") {
		assert.Contains(t, err.Error(), embeddingRequired)
	}

	// (d) Control: the rule is in force for a change to the model itself.
	err = e.patch(remodelled, `{"model":{"name":"MiniMax-M3"}}`)
	require.Error(t, err, "a model changed without an embedding was accepted")
	assert.True(t, apierrors.IsInvalid(err), "%v", err)
	assert.Contains(t, err.Error(), embeddingRequired)
}
