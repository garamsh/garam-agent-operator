package renderer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// The renderer writes Agents, so it is tested against envtest's API server,
// which applies the CRD's defaulting and validation a fake client would not.
var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	k8sClient client.Client
)

func TestRenderer(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Renderer Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	Expect(agentv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	cancel()
	Eventually(func() error { return testEnv.Stop() }, time.Minute, time.Second).Should(Succeed())
})

// firstEnvTestBinaryDir finds the binaries `make setup-envtest` downloads, so
// the suite runs from an IDE without KUBEBUILDER_ASSETS set.
func firstEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}

	return ""
}
