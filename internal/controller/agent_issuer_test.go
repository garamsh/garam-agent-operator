package controller

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// testGaramIssuer is the issuer the specs give the manager: the dev cluster's
// machine.issuer (gitops#298), an origin other than testGaramAddress, as it is
// in-cluster.
const testGaramIssuer = "https://machine.dev.garam.sh"

// reconcileAgentWithIssuer runs one reconcile for the named Agent, with this
// operator placing garam's adapter where adapter is true, naming the control
// service the adapter activates through, and giving the manager issuer.
func reconcileAgentWithIssuer(name, issuer string, adapter bool) (reconcile.Result, error) {
	GinkgoHelper()

	rootFile := filepath.Join(GinkgoT().TempDir(), "control-root.pem")
	Expect(os.WriteFile(rootFile, []byte(testControlRoot), 0o600)).To(Succeed())
	reconciler := &AgentReconciler{
		Client:          k8sClient,
		Scheme:          k8sClient.Scheme(),
		CopyImage:       testCopyImage,
		GaramAddress:    testGaramAddress,
		GaramIssuer:     issuer,
		ControlAddress:  testControlAddress,
		ControlRootFile: rootFile,
	}
	if adapter {
		reconciler.AdapterImage = testAdapterImage
	}

	return runReconcile(name, reconciler)
}

// renderedConfig is the config file the Pod's config writer writes, read back,
// and empty where the Pod writes none.
func renderedConfig(pod corev1.PodSpec) map[string]any {
	GinkgoHelper()

	written := map[string]any{}
	for _, container := range pod.InitContainers {
		if container.Name == configContainerName {
			Expect(yaml.Unmarshal([]byte(environmentOf(container)[configContentVariable]), &written)).To(Succeed())
		}
	}

	return written
}

var _ = Describe("Message issuer", func() {
	// ADR 0071: one static entry, the keys fetched from the machine listener the
	// adapter is given and verified against the garam server root the agent
	// mounts out of its credential Secret (ADR 0073).
	It("renders garam's issuer, its keys under the adapter's garam address, and the server root the agent mounts", func() {
		name := "issuer-rendered"
		agentFrom(name)
		_, err := reconcileAgentWithIssuer(name, testGaramIssuer, true)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec

		Expect(renderedConfig(pod)).To(HaveKeyWithValue("issuers", ConsistOf(map[string]any{
			"issuer":       testGaramIssuer,
			"keys-url":     "https://" + testGaramAddress + "/message-signing-keys",
			"keys-ca-file": "/run/sherlock/credentials/" + garam.ServerRootKey,
		})))

		By("the keys-ca-file in the server root the agent container mounts, and nothing else of its credential")
		Expect(containerOf(pod, agentContainerName).VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: serverRootVolumeName, MountPath: agentTypeSherlock.credentialsMountPath, ReadOnly: true}))
		Expect(agentTypeSherlock.credentialsMountPath).To(Equal("/run/sherlock/credentials"))
		Expect(volumeNamed(pod, serverRootVolumeName).Secret.Items).To(Equal(
			[]corev1.KeyToPath{{Key: garam.ServerRootKey, Path: garam.ServerRootKey}}))
		By("the same file the adapter reads as its server root, from the same Secret")
		adapter := initContainerOf(pod, adapterContainerName)
		Expect(environmentOf(adapter)).To(HaveKeyWithValue(adapterServerRootSetting,
			adapterCredentialsMountPath+"/"+garam.ServerRootKey))
		Expect(volumeNamed(pod, credentialsVolumeName).Secret.SecretName).
			To(Equal(volumeNamed(pod, serverRootVolumeName).Secret.SecretName))
	})

	It("renders the issuer byte for byte, normalising nothing sherlock compares exactly", func() {
		// A value any URL normalisation would change: a trailing slash and an
		// upper-case host.
		const unnormalised = "https://Machine.Dev.Garam.sh/"
		name := "issuer-verbatim"
		agentFrom(name)
		_, err := reconcileAgentWithIssuer(name, unnormalised, true)
		Expect(err).NotTo(HaveOccurred())

		Expect(renderedConfig(statefulSetFor(name).Spec.Template.Spec)).To(HaveKeyWithValue("issuers",
			ConsistOf(HaveKeyWithValue("issuer", unnormalised))))
	})

	It("renders no issuer where the manager names none, or where the agent's Pod carries no adapter", func() {
		name := "issuer-absent"
		agentFrom(name)

		By("the control: with the issuer and the adapter, the entry is rendered")
		_, err := reconcileAgentWithIssuer(name, testGaramIssuer, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(renderedConfig(statefulSetFor(name).Spec.Template.Spec)).To(HaveKey("issuers"))

		By("no issuer named: no entry, and the Pod as it was before this decision")
		_, err = reconcileAgentWithIssuer(name, "", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(renderedConfig(statefulSetFor(name).Spec.Template.Spec)).NotTo(HaveKey("issuers"))
		Expect(initContainerOf(statefulSetFor(name).Spec.Template.Spec, adapterContainerName).Name).
			To(Equal(adapterContainerName))

		By("an issuer named but no adapter placed: no entry")
		_, err = reconcileAgentWithIssuer(name, testGaramIssuer, false)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers).NotTo(ContainElement(HaveField("Name", adapterContainerName)))
		Expect(renderedConfig(pod)).NotTo(HaveKey("issuers"))
	})
})
