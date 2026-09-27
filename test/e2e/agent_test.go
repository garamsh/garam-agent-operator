//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/test/utils"
)

const (
	// agentTestNamespace holds the Agent under test and everything it produces,
	// so that removing it removes all of them.
	agentTestNamespace = "sherlock-e2e"

	agentUnderTest    = "e2e-agent"
	agentPod          = agentUnderTest + "-0"
	credentialsSecret = agentUnderTest + "-credentials"

	// agentNeverStarts differs from the Agent under test in its image alone,
	// and that image resolves nowhere: the operator reconciles its workload and
	// the cluster never runs one behind it. It is what makes a green readiness
	// condition mean anything — the same operator, the same namespace, the same
	// spec but for one field, reporting the opposite.
	agentNeverStarts = "e2e-agent-never-starts"

	// unstartableImage names a host no registry can come to serve: .invalid is
	// reserved for that (RFC 2606 section 2).
	unstartableImage = "sherlock.invalid/agent:v0"

	// credentialsToken is what the specs look for: in the mounted file, and
	// nowhere in the container's environment.
	credentialsToken = "e2e-token-6f1c9a"

	// credentialsMountPath is where the agent reads the copy the init container
	// made, not where the kubelet projects the Secret.
	credentialsMountPath = "/run/sherlock/credentials"
	stateMountPath       = "/var/lib/sherlock"
)

// keyfileRule is the rule garam's reader applies to a key file, transcribed from
// garam@1ff8346:internal/keyfile/keyfile.go:30-41: it refuses a file any group or
// other permission bit is set on, and refuses one whose owner is not the reading
// process. 63 is 0o077 in decimal, which sh arithmetic has no octal literal for.
const keyfileRule = `
check() {
  perm=$(( 0$(stat -Lc '%a' "$1") ))
  if [ $(( perm & 63 )) -ne 0 ]; then
    echo "$2 REFUSE readable-beyond-its-owner"
  elif [ "$(stat -Lc '%u' "$1")" != "$(id -u)" ]; then
    echo "$2 REFUSE owned-by-another-user"
  else
    echo "$2 ACCEPT"
  fi
}
`

// agentManifestFor renders an Agent differing from every other this suite
// creates in its name and its image, so that a difference in what the operator
// reports traces to the image and to nothing else.
func agentManifestFor(name, image string) string {
	return fmt.Sprintf(`
apiVersion: agent.garam.sh/v1alpha1
kind: Agent
metadata:
  name: %s
  namespace: %s
spec:
  image: %s
  credentialsSecretName: %s
  storageSize: 64Mi
`, name, agentTestNamespace, image, credentialsSecret)
}

// agentManifest is the Agent under test. Its image is one a registry serves,
// because the operator pulls every container of the Pod at every start.
var agentManifest = agentManifestFor(agentUnderTest, agentImage)

// kubectlIn runs kubectl against the namespace the Agent under test lives in.
// The namespace goes in front: after the `--` of an exec it would be an argument
// to the command inside the container instead.
func kubectlIn(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", agentTestNamespace}, args...)...))
}

// waitForAgentPod blocks until the Agent's Pod is running, which is what reading
// anything out of that container depends on. It is the subject of the first spec
// and a precondition of the rest, so each one states it rather than inheriting
// it from the spec before.
func waitForAgentPod() {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		phase, err := kubectlIn("get", "pod", agentPod, "-o", "jsonpath={.status.phase}")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(phase).To(Equal("Running"))
	}, 3*time.Minute, time.Second).Should(Succeed())
}

// agentCondition reads one field of one condition off an Agent. An absent
// condition reads as the empty string, which no assertion here accepts.
func agentCondition(agent, conditionType, field string) (string, error) {
	return kubectlIn("get", "agent", agent, "-o",
		fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].%s}`, conditionType, field))
}

// execInAgent runs a command inside the agent container of the Agent's Pod.
func execInAgent(args ...string) (string, error) {
	return kubectlIn(append([]string{"exec", agentPod, "-c", "agent", "--"}, args...)...)
}

var _ = Describe("Agent workload", Ordered, func() {
	BeforeAll(func() {
		By("creating the namespace for the Agent under test")
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", agentTestNamespace))
		Expect(err).NotTo(HaveOccurred(), "Failed to create the namespace")

		// The manager's namespace enforces the standard on the operator; this is
		// the one the workload it builds runs in. Enforcing it here is what makes
		// the suite refuse a Pod the standard refuses.
		By("labeling the namespace to enforce the restricted security policy")
		_, err = utils.Run(exec.Command("kubectl", "label", "--overwrite", "ns", agentTestNamespace,
			"pod-security.kubernetes.io/enforce=restricted"))
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("creating the credentials Secret the Agent names")
		_, err = kubectlIn("create", "secret", "generic", credentialsSecret,
			"--from-literal=token="+credentialsToken)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the credentials Secret")

		By("creating the Agent")
		apply := exec.Command("kubectl", "apply", "-f", "-")
		apply.Stdin = strings.NewReader(agentManifest)
		_, err = utils.Run(apply)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the Agent")
	})

	AfterAll(func() {
		By("removing the namespace and everything the Agent produced in it")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", agentTestNamespace,
			"--ignore-not-found", "--timeout=2m"))
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}

		for _, args := range [][]string{
			{"get", "agent", agentUnderTest, "-o", "yaml"},
			{"describe", "statefulset", agentUnderTest},
			{"describe", "pod", agentPod},
			{"get", "events", "--sort-by=.lastTimestamp"},
		} {
			if output, err := kubectlIn(args...); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "%s:\n%s\n", strings.Join(args, " "), output)
			}
		}
	})

	It("starts the Pod its workload describes and reports Synced on the Agent", func() {
		By("waiting for the Pod to reach Running")
		waitForAgentPod()

		By("reading the Synced condition the operator wrote")
		Eventually(func(g Gomega) {
			status, err := agentCondition(agentUnderTest, agentv1alpha1.ConditionSynced, "status")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal("True"))
		}, time.Minute, time.Second).Should(Succeed())
	})

	It("reports a workload the cluster never ran, beside one it did", func() {
		By("creating an Agent differing from the one under test in its image alone")
		apply := exec.Command("kubectl", "apply", "-f", "-")
		apply.Stdin = strings.NewReader(agentManifestFor(agentNeverStarts, unstartableImage))
		_, err := utils.Run(apply)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the Agent that cannot start")
		DeferCleanup(func() {
			_, _ = kubectlIn("delete", "agent", agentNeverStarts, "--ignore-not-found", "--timeout=2m")
		})

		// This is the reported defect written as a spec: the operator wrote the
		// workload it was asked for, so Synced is True and stays True, and
		// nothing has ever run behind it.
		By("reading both conditions on the Agent no image serves")
		Eventually(func(g Gomega) {
			synced, err := agentCondition(agentNeverStarts, agentv1alpha1.ConditionSynced, "status")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(synced).To(Equal("True"))

			available, err := agentCondition(agentNeverStarts, agentv1alpha1.ConditionAvailable, "status")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(available).To(Equal("False"))

			reason, err := agentCondition(agentNeverStarts, agentv1alpha1.ConditionAvailable, "reason")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(reason).To(Equal(agentv1alpha1.ReasonReplicaNotReady))
		}, 2*time.Minute, time.Second).Should(Succeed())

		// The control, and the reason this spec is here rather than only in the
		// envtest suite: a kubelet decided this replica is ready. Below this
		// layer the field is only ever what a spec wrote into it, so False
		// there is consistent with a controller that reads nothing.
		By("reading the same condition on the Agent whose Pod does run")
		waitForAgentPod()
		Eventually(func(g Gomega) {
			available, err := agentCondition(agentUnderTest, agentv1alpha1.ConditionAvailable, "status")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(available).To(Equal("True"))

			reason, err := agentCondition(agentUnderTest, agentv1alpha1.ConditionAvailable, "reason")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(reason).To(Equal(agentv1alpha1.ReasonReplicaReady))
		}, 2*time.Minute, time.Second).Should(Succeed())
	})

	It("delivers the credential as a file the rule its reader applies accepts, and nowhere in the environment", func() {
		waitForAgentPod()

		// Everything below is about an agent that dropped root. Read as root, a
		// credential file is readable whatever mode it carries, so this is the
		// line that makes the rest of the spec mean anything.
		By("checking the container is not running as root")
		uid, err := execInAgent("id", "-u")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(uid)).NotTo(Equal("0"))

		By("applying garam's rule to the credential, and to a file written beside it")
		// The control is the second file: written by this container, into a
		// volume it can write, at the mode a default umask gives it. The rule only
		// refuses, so a control it accepts would leave a check that cannot fail
		// reading exactly like one that can.
		verdicts, err := execInAgent("sh", "-ec", keyfileRule+
			"umask 022\n"+
			": > "+stateMountPath+"/control\n"+
			"check "+credentialsMountPath+"/token credential\n"+
			"check "+stateMountPath+"/control control\n")
		Expect(err).NotTo(HaveOccurred(), "the credentials are not files inside the container")
		Expect(verdicts).To(ContainSubstring("credential ACCEPT"))
		Expect(verdicts).To(ContainSubstring("control REFUSE readable-beyond-its-owner"))

		By("reading the credential out of the file")
		content, err := execInAgent("cat", credentialsMountPath+"/token")
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal(credentialsToken))

		By("listing the container's environment")
		environment, err := execInAgent("env")
		Expect(err).NotTo(HaveOccurred())

		// The listing has to be shown to carry what is there before its silence
		// about the credential means anything: HOSTNAME is the Pod's name, so an
		// empty listing, or one from another container, fails here first.
		Expect(environment).To(ContainSubstring("HOSTNAME=" + agentPod))
		Expect(environment).NotTo(ContainSubstring(credentialsToken))
		Expect(environment).NotTo(ContainSubstring("token="))
	})

	It("gives the agent a state volume it can write to as that user", func() {
		waitForAgentPod()

		// Whether this needs the Pod's group depends on the provisioner. Kind's
		// local-path creates the directory world-writable, so this passes with or
		// without one here — measured, not assumed. What it does show is that the
		// agent can write the volume it was given as the user it runs as, which
		// no layer below this one can say at all.
		By("writing a file to the state volume and reading it back")
		written, err := execInAgent("sh", "-c",
			"echo written-by-$(id -u) > "+stateMountPath+"/probe && cat "+stateMountPath+"/probe")
		Expect(err).NotTo(HaveOccurred(), "the agent cannot write to its state volume")

		uid, err := execInAgent("id", "-u")
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(written)).To(Equal("written-by-" + strings.TrimSpace(uid)))
	})

	It("removes the StatefulSet and its Pod when the Agent is deleted", func() {
		// Without this the workload might not exist yet, and a spec that asserts
		// its absence would pass having never seen it.
		waitForAgentPod()

		By("deleting the Agent")
		_, err := kubectlIn("delete", "agent", agentUnderTest, "--timeout=2m")
		Expect(err).NotTo(HaveOccurred())

		By("waiting for the garbage collector to follow the owner reference")
		for kind, name := range map[string]string{"statefulset": agentUnderTest, "pod": agentPod} {
			Eventually(func(g Gomega) {
				output, err := kubectlIn("get", kind, name)
				g.Expect(err).To(HaveOccurred(), "%s %s still exists", kind, name)
				g.Expect(output).To(ContainSubstring("NotFound"))
			}, 2*time.Minute, time.Second).Should(Succeed())
		}
	})
})
