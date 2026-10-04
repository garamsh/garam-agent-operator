//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/test/utils"
)

// agentImage is what the Agent under test runs. It is not sherlock's agent,
// which the suite cannot run, but a stand-in built from test/e2e/agent-image:
// its entrypoint accepts the command line this operator gives sherlock and
// refuses one carrying no agent ID, as sherlock does, and then stays up. It
// keeps a shell that can read the mounted credentials, and runs as the
// non-root user the Pod names. That last part is not a detail: an image that
// keeps root reads a root-owned credential file whatever mode it carries, which
// is how #31 stayed invisible through every layer.
//
// It is served from a registry on the node's loopback interface because the
// operator pulls every container of an agent's Pod at every start: an image
// only loaded onto the node would leave the Pod reaching for a registry that
// does not serve it. containerd pulls a localhost registry over plain HTTP, so
// the node needs no registry configuration.
const agentImage = agentImageRegistry + "/garam-e2e-agent:v0"

// agentImageRegistry is where the registry serving agentImage listens, on the
// network of the node it runs on.
const agentImageRegistry = "localhost:5000"

// agentImageRegistryNamespace holds the registry and nothing else, so that
// removing it removes the registry. It enforces no PodSecurity level, because
// the registry has to share the node's network.
const agentImageRegistryNamespace = "e2e-agent-image-registry"

// agentImageRegistryManifest is the registry the node pulls agentImage from. Its
// image is pinned to the multi-platform index.
var agentImageRegistryManifest = fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: registry
  namespace: %s
spec:
  hostNetwork: true
  containers:
  - name: registry
    image: registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8
    env:
    - name: REGISTRY_HTTP_ADDR
      value: %s
`, agentImageRegistryNamespace, agentImageRegistry)

// serveAgentImage builds agentImage, starts the registry, and pushes the image
// into it from inside every node of the cluster.
func serveAgentImage() {
	GinkgoHelper()

	By("building the stand-in agent image")
	projectDir, err := utils.GetProjectDir()
	Expect(err).NotTo(HaveOccurred())
	_, err = utils.Run(exec.Command(containerTool(), "build", "-t", agentImage,
		projectDir+"/test/e2e/agent-image"))
	Expect(err).NotTo(HaveOccurred(), "Failed to build the stand-in agent image")

	By("loading it on Kind")
	Expect(utils.LoadImageToKindClusterWithName(agentImage)).To(Succeed(),
		"Failed to load the stand-in agent image into Kind")

	By("starting the registry it is pulled from")
	_, err = utils.Run(exec.Command("kubectl", "create", "ns", agentImageRegistryNamespace))
	Expect(err).NotTo(HaveOccurred(), "Failed to create the registry's namespace")
	apply := exec.Command("kubectl", "apply", "-f", "-")
	apply.Stdin = strings.NewReader(agentImageRegistryManifest)
	_, err = utils.Run(apply)
	Expect(err).NotTo(HaveOccurred(), "Failed to create the registry")
	_, err = utils.Run(exec.Command("kubectl", "-n", agentImageRegistryNamespace, "wait", "pod/registry",
		"--for=condition=Ready", "--timeout=3m"))
	Expect(err).NotTo(HaveOccurred(), "The registry did not become ready")

	nodes, err := utils.Run(exec.Command(utils.KindBinary(), "get", "nodes", "--name", utils.KindCluster()))
	Expect(err).NotTo(HaveOccurred(), "Failed to list the Kind nodes")
	for _, node := range utils.GetNonEmptyLines(nodes) {
		By("pushing it into the registry from node " + node)
		// The registry answers once it listens, which Ready does not wait for.
		Eventually(func() error {
			_, err := utils.Run(exec.Command(containerTool(), "exec", node,
				"ctr", "--namespace=k8s.io", "images", "push", "--plain-http", agentImage))
			return err
		}, time.Minute, 2*time.Second).Should(Succeed(), "Failed to push the stand-in agent image")
	}
}

// removeAgentImageRegistry removes what serveAgentImage started in the cluster.
func removeAgentImageRegistry() {
	By("removing the stand-in agent image's registry")
	_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", agentImageRegistryNamespace,
		"--ignore-not-found", "--timeout=2m"))
}

// containerTool is the container CLI the Makefile builds images with.
func containerTool() string {
	if v, ok := os.LookupEnv("CONTAINER_TOOL"); ok {
		return v
	}
	return "docker"
}
