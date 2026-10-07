//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/garamsh/garam-agent-operator/test/utils"
)

// moveNamespace holds the Agents whose memory the suite moves, apart from the
// Agent under test elsewhere, so that deleting it removes every claim and Job a
// move made.
const moveNamespace = "memory-move-e2e"

// moveCredentials is the credentials Secret both moved Agents name.
const moveCredentials = "move-credentials"

// writeMemory is the shell that writes a memory store, its write-ahead log and
// an outbox entry the way an agent leaves them, and prints their digests.
const writeMemory = `set -eu
mkdir -p ` + memoryDir + `
cd ` + memoryDir + `
head -c 65536 /dev/urandom > memory.db
head -c 4096 /dev/urandom > memory.db-wal
mkdir -p outbox
head -c 512 /dev/urandom > outbox/entry-1.json
sha256sum memory.db memory.db-wal outbox/entry-1.json`

// readMemory prints the digests writeMemory printed, of what is there now.
const readMemory = `cd ` + memoryDir + ` && sha256sum memory.db memory.db-wal outbox/entry-1.json`

// kubectlMove runs kubectl in moveNamespace.
func kubectlMove(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", moveNamespace}, args...)...))
}

// applyMovedAgent creates an Agent in moveNamespace and waits for its Pod.
func applyMovedAgent(name string) {
	GinkgoHelper()

	apply := exec.Command("kubectl", "apply", "-f", "-")
	apply.Stdin = strings.NewReader(fmt.Sprintf(`
apiVersion: agent.garam.sh/v1alpha1
kind: Agent
metadata:
  name: %s
  namespace: %s
spec:
  image: %s
  credentialsSecretName: %s
  storageSize: 64Mi
`, name, moveNamespace, agentImage, moveCredentials))
	_, err := utils.Run(apply)
	Expect(err).NotTo(HaveOccurred(), "Failed to create Agent %s", name)
	waitForMovedPod(name)
}

// waitForMovedPod waits until the Agent's Pod runs with its agent ready.
func waitForMovedPod(name string) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		ready, err := kubectlMove("get", "pod", name+"-0", "-o",
			`jsonpath={.status.containerStatuses[?(@.name=="agent")].ready}`)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(ready).To(Equal("true"))
	}, 3*time.Minute, time.Second).Should(Succeed())
}

// inMovedAgent runs a shell command in the Agent's agent container.
func inMovedAgent(name, script string) (string, error) {
	return kubectlMove("exec", name+"-0", "-c", "agent", "--", "sh", "-c", script)
}

// askToMove sets the Agent's memory move.
func askToMove(name, id string) {
	GinkgoHelper()

	_, err := kubectlMove("patch", "agent", name, "--type=merge", "-p",
		fmt.Sprintf(`{"spec":{"memoryMove":{"id":%q,"storageSize":"64Mi"}}}`, id))
	Expect(err).NotTo(HaveOccurred())
}

// moveReason is the reason of the Agent's MemoryMove condition.
func moveReason(name string) (string, error) {
	return kubectlMove("get", "agent", name, "-o", `jsonpath={.status.conditions[?(@.type=="MemoryMove")].reason}`)
}

var _ = Describe("Memory move", Ordered, func() {
	BeforeAll(func() {
		By("creating the namespace the moved Agents run in, enforcing the restricted policy")
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", moveNamespace))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "label", "--overwrite", "ns", moveNamespace,
			"pod-security.kubernetes.io/enforce=restricted"))
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectlMove("create", "secret", "generic", moveCredentials, "--from-literal=token="+credentialsToken)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		By("deleting the moved Agents while the manager runs, then their namespace")
		for _, name := range []string{"move-kept", "move-refused"} {
			_, _ = kubectlMove("delete", "agent", name, "--ignore-not-found", "--timeout=3m")
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", moveNamespace, "--ignore-not-found", "--timeout=3m"))
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"get", "agent", "-o", "yaml"},
			{"get", "pvc,jobs,pods", "-o", "wide"},
			{"get", "pvc", "-o", "yaml"},
			{"logs", "-l", "app.kubernetes.io/name=agent-memory-move", "--tail=50"},
			{"get", "events", "--sort-by=.lastTimestamp"},
		} {
			if output, err := kubectlMove(args...); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "%s:\n%s\n", strings.Join(args, " "), output)
			}
		}
	})

	It("moves an agent's memory to a new claim byte for byte, and the agent accepts it before it serves", func() {
		const name = "move-kept"
		applyMovedAgent(name)
		written, err := inMovedAgent(name, writeMemory)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(written, "\n")).To(Equal(3), "the memory was not written: %s", written)

		By("moving it")
		askToMove(name, "one")
		Eventually(func(g Gomega) {
			g.Expect(moveReason(name)).To(Equal("Accepted"))
		}, 6*time.Minute, 2*time.Second).Should(Succeed())

		By("reading the same bytes on the claim the agent now runs on")
		claim, err := kubectlMove("get", "pod", name+"-0", "-o",
			`jsonpath={.spec.volumes[?(@.name=="state")].persistentVolumeClaim.claimName}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(claim).To(Equal(name + "-state-one"))
		args, err := kubectlMove("get", "pod", name+"-0", "-o", `jsonpath={.spec.containers[?(@.name=="agent")].args}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(ContainSubstring("--require-store"))
		read, err := inMovedAgent(name, readMemory)
		Expect(err).NotTo(HaveOccurred())
		Expect(read).To(Equal(written))

		By("keeping the claim the memory moved off, naming the claim it moved to")
		movedTo, err := kubectlMove("get", "pvc", "state-"+name+"-0", "-o",
			`jsonpath={.metadata.annotations.agent\.garam\.sh/moved-to}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(movedTo).To(Equal(name + "-state-one"))
	})

	It("refuses a copy its verification does not match, keeping the old claim in use and activating nothing", func() {
		const name = "move-refused"
		applyMovedAgent(name)
		written, err := inMovedAgent(name, writeMemory)
		Expect(err).NotTo(HaveOccurred())

		By("marking the source so that the copy image's cp corrupts the copy")
		_, err = inMovedAgent(name, ": > "+stateMountPath+"/e2e-corrupt-copy")
		Expect(err).NotTo(HaveOccurred())

		askToMove(name, "one")
		Eventually(func(g Gomega) {
			g.Expect(moveReason(name)).To(Equal("VerificationFailed"))
		}, 6*time.Minute, 2*time.Second).Should(Succeed())

		By("leaving the agent stopped on its old claim, with no Pod")
		replicas, err := kubectlMove("get", "statefulset", name, "-o", "jsonpath={.spec.replicas}")
		Expect(err).NotTo(HaveOccurred())
		Expect(replicas).To(Equal("0"))
		Eventually(func(g Gomega) {
			pods, err := kubectlMove("get", "pod", name+"-0", "--ignore-not-found", "-o", "name")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(pods).To(BeEmpty())
		}, 2*time.Minute, time.Second).Should(Succeed())
		annotations, err := kubectlMove("get", "pvc", "state-"+name+"-0", "-o", "jsonpath={.metadata.annotations}")
		Expect(err).NotTo(HaveOccurred())
		Expect(annotations).NotTo(ContainSubstring("moved-to"))
		phase, err := kubectlMove("get", "pvc", name+"-state-one", "-o",
			`jsonpath={.metadata.annotations.agent\.garam\.sh/move-phase}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(phase).To(Equal("refused"))

		By("starting it again on the untouched old claim once the move is cleared")
		_, err = kubectlMove("patch", "agent", name, "--type=json", "-p", `[{"op":"remove","path":"/spec/memoryMove"}]`)
		Expect(err).NotTo(HaveOccurred())
		waitForMovedPod(name)
		read, err := inMovedAgent(name, readMemory)
		Expect(err).NotTo(HaveOccurred())
		Expect(read).To(Equal(written))
	})
})
