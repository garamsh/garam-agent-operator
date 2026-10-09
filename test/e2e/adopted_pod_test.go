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

// adoptedNamespace holds the agent whose shared-shape StatefulSet the manager
// replaces, and nothing else, so that scaling the manager down disturbs no
// other spec's agent.
const (
	adoptedNamespace   = "adopted-pod-e2e"
	adoptedAgent       = "adopted"
	adoptedPod         = adoptedAgent + "-0"
	adoptedCredentials = "adopted-credentials"

	podResource         = "pod"
	statefulSetResource = "statefulset"
)

// sharedShapeStatefulSet is the shape an operator before ADR 0044 built for the
// Agent whose UID is agentUID: the workspace on the state claim, under its
// subPath, and no claim of its own. It carries what that operator's carried and
// the replacement relies on: the Agent as its controller, whose deletion event
// wakes the Agent; this operator's selector, so the next StatefulSet adopts its
// Pod; and the writer fence's finalizer on its template (ADR 0042).
func sharedShapeStatefulSet(agentUID string) string {
	return fmt.Sprintf(`
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %[1]s
  namespace: %[2]s
  ownerReferences:
    - apiVersion: agent.garam.sh/v1alpha1
      kind: Agent
      name: %[1]s
      uid: %[4]s
      controller: true
      blockOwnerDeletion: true
spec:
  replicas: 1
  serviceName: ""
  selector:
    matchLabels:
      app.kubernetes.io/name: agent
      app.kubernetes.io/instance: %[1]s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: agent
        app.kubernetes.io/instance: %[1]s
      finalizers: ["agent.garam.sh/writer-stopped"]
    spec:
      containers:
        - name: agent
          image: %[3]s
          args: ["agent", "--agent-id", "%[1]s"]
          volumeMounts:
            - name: state
              mountPath: /var/lib/sherlock
        - name: workspace
          image: %[3]s
          volumeMounts:
            - name: state
              mountPath: /var/lib/sherlock/workspace
              subPath: workspace
  volumeClaimTemplates:
    - metadata:
        name: state
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 64Mi
`, adoptedAgent, adoptedNamespace, agentImage, agentUID)
}

func kubectlAdopted(args ...string) (string, error) {
	return utils.Run(exec.Command("kubectl", append([]string{"-n", adoptedNamespace}, args...)...))
}

func applyAdopted(manifest string) {
	GinkgoHelper()

	apply := exec.Command("kubectl", "apply", "-n", adoptedNamespace, "-f", "-")
	apply.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(apply)
	Expect(err).NotTo(HaveOccurred())
}

// scaleManager sets the manager's replicas and waits for the rollout.
func scaleManager(replicas int) {
	GinkgoHelper()

	_, err := utils.Run(exec.Command("kubectl", "-n", namespace, "scale", "deployment", deploymentName,
		fmt.Sprintf("--replicas=%d", replicas)))
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(g Gomega) {
		ready, err := utils.Run(exec.Command("kubectl", "-n", namespace, kubectlGet, "deployment", deploymentName,
			"-o", "jsonpath={.status.replicas}/{.status.readyReplicas}"))
		g.Expect(err).NotTo(HaveOccurred())
		if replicas == 0 {
			g.Expect(ready).To(Equal("/"), "the manager still runs")
		} else {
			g.Expect(ready).To(Equal(fmt.Sprintf("%d/%d", replicas, replicas)))
		}
	}, 3*time.Minute, time.Second).Should(Succeed())
}

// adoptedPodState is what the spec reads of the Pod, its StatefulSet and the
// Agent at once.
type adoptedPodState struct {
	podUID, podOwner, podRevision   string
	setUID, updateRevision          string
	synced, syncedReason, available string
}

// readAdopted reads one field of one object in the namespace, empty where the
// object is absent.
func readAdopted(g Gomega, resource, name, path string) string {
	value, err := kubectlAdopted(kubectlGet, resource, name, "--ignore-not-found", "-o", "jsonpath="+path)
	g.Expect(err).NotTo(HaveOccurred())

	return value
}

func readAdoptedState(g Gomega) adoptedPodState {
	condition := func(conditionType, field string) string {
		return readAdopted(g, agentResource, adoptedAgent,
			fmt.Sprintf(`{.status.conditions[?(@.type=="%s")].%s}`, conditionType, field))
	}

	return adoptedPodState{
		podUID:         readAdopted(g, podResource, adoptedPod, "{.metadata.uid}"),
		podOwner:       readAdopted(g, podResource, adoptedPod, "{.metadata.ownerReferences[0].uid}"),
		podRevision:    readAdopted(g, podResource, adoptedPod, "{.metadata.labels.controller-revision-hash}"),
		setUID:         readAdopted(g, statefulSetResource, adoptedAgent, "{.metadata.uid}"),
		updateRevision: readAdopted(g, statefulSetResource, adoptedAgent, "{.status.updateRevision}"),
		synced:         condition("Synced", "status"),
		syncedReason:   condition("Synced", "reason"),
		available:      condition("Available", "status"),
	}
}

var _ = Describe("Adopted Pod", Ordered, func() {
	BeforeAll(func() {
		_, err := utils.Run(exec.Command("kubectl", "create", "ns", adoptedNamespace))
		Expect(err).NotTo(HaveOccurred())
		_, err = kubectlAdopted("create", "secret", "generic", adoptedCredentials, "--from-literal=token=adopted")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		// The manager runs again before the Agent is deleted: only it releases
		// the writer fence's finalizer (#287).
		scaleManager(1)
		_, _ = kubectlAdopted("delete", agentResource, adoptedAgent, "--ignore-not-found", "--timeout=3m")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", adoptedNamespace, "--ignore-not-found", "--timeout=2m"))
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{kubectlGet, "agent,statefulset,pod,controllerrevisions", "-o=yaml"},
			{kubectlGet, "event", "--sort-by=.metadata.creationTimestamp"},
		} {
			if output, err := kubectlAdopted(args...); err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "%s:\n%s\n", strings.Join(args, " "), output)
			}
		}
	})

	// #340: at 4c425a7, the StatefulSet that replaced a shared-shape one adopted
	// its Pod, recorded the rollout complete, and left the Pod on the old
	// revision while Synced and Available reported success.
	It("rolls the Pod a replacing StatefulSet adopts onto its revision, reporting nothing reconciled until then", func() {
		By("stopping the manager, so the shared shape is in place before it reconciles, as on an upgrade")
		scaleManager(0)

		By("creating the Agent, which nothing reconciles yet")
		applyAdopted(fmt.Sprintf(`
apiVersion: agent.garam.sh/v1alpha1
kind: Agent
metadata:
  name: %s
spec:
  image: %s
  credentialsSecretName: %s
  storageSize: 64Mi
`, adoptedAgent, agentImage, adoptedCredentials))
		agentUID, err := kubectlAdopted(kubectlGet, agentResource, adoptedAgent, "-o", "jsonpath={.metadata.uid}")
		Expect(err).NotTo(HaveOccurred())
		Expect(agentUID).NotTo(BeEmpty())

		By("running the Pod of a shared-shape StatefulSet the Agent owns")
		applyAdopted(sharedShapeStatefulSet(agentUID))
		Eventually(func(g Gomega) {
			g.Expect(readAdopted(g, podResource, adoptedPod, `{.status.conditions[?(@.type=="Ready")].status}`)).
				To(Equal("True"))
		}, 3*time.Minute, time.Second).Should(Succeed())
		var before adoptedPodState
		Eventually(func(g Gomega) {
			before = readAdoptedState(g)
			g.Expect(before.podRevision).NotTo(BeEmpty())
			g.Expect(before.setUID).NotTo(BeEmpty())
		}, time.Minute, time.Second).Should(Succeed())

		By("starting the manager")
		scaleManager(1)

		By("waiting for the replacing StatefulSet to exist")
		Eventually(func(g Gomega) {
			state := readAdoptedState(g)
			g.Expect(state.setUID).NotTo(BeEmpty())
			g.Expect(state.setUID).NotTo(Equal(before.setUID))
			g.Expect(state.updateRevision).NotTo(BeEmpty())
		}, 3*time.Minute, time.Second).Should(Succeed())

		By("reading, until a Pod of the new revision runs, that the Agent never reports the old one reconciled")
		var misreported []string
		Eventually(func(g Gomega) {
			state := readAdoptedState(g)
			onOldRevision := state.podUID != "" && state.podRevision != state.updateRevision
			if onOldRevision && (state.synced == "True" || state.available == "True") {
				misreported = append(misreported, fmt.Sprintf("pod %s on %s, set at %s: Synced=%s (%s) Available=%s",
					state.podUID, state.podRevision, state.updateRevision, state.synced, state.syncedReason,
					state.available))
			}
			g.Expect(state.podUID).NotTo(BeEmpty())
			g.Expect(state.podUID).NotTo(Equal(before.podUID), "the adopted Pod was never rolled")
			g.Expect(state.podRevision).To(Equal(state.updateRevision))
			g.Expect(state.podOwner).To(Equal(state.setUID))
		}, 4*time.Minute, 2*time.Second).Should(Succeed())
		Expect(misreported).To(BeEmpty(), "the Agent reported a Pod on another revision reconciled")

		By("reporting the rolled workload reconciled and ready")
		Eventually(func(g Gomega) {
			state := readAdoptedState(g)
			g.Expect(state.synced).To(Equal("True"))
			g.Expect(state.available).To(Equal("True"))
		}, 3*time.Minute, time.Second).Should(Succeed())

		By("keeping the state claim the shared shape made")
		Expect(kubectlAdopted(kubectlGet, "pvc", "state-"+adoptedPod, "-o", "jsonpath={.metadata.name}")).
			To(Equal("state-" + adoptedPod))
	})
})
