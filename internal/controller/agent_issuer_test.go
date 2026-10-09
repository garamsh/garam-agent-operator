package controller

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// testGaramIssuer is the issuer the specs give the manager: an origin other
// than testGaramAddress, as garam's machine.issuer is in-cluster.
const testGaramIssuer = "https://garam.example.com"

// reconcileAgentWithIssuer runs one reconcile for the named Agent, with this
// operator placing garam's adapter where adapter is true, naming the control
// service a Control-source agent's adapter activates through, and giving the
// manager issuer.
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
	// already mounts.
	It("renders garam's issuer, its keys under the adapter's garam address, and the server root the agent mounts, on either source", func() {
		for _, source := range []agentv1alpha1.DesiredSource{agentv1alpha1.DesiredSourceGaram, agentv1alpha1.DesiredSourceControl} {
			By("an agent on the " + string(source) + " source")
			name := "issuer-" + map[agentv1alpha1.DesiredSource]string{
				agentv1alpha1.DesiredSourceGaram: "garam", agentv1alpha1.DesiredSourceControl: "control",
			}[source]
			agentFrom(name, source)
			_, err := reconcileAgentWithIssuer(name, testGaramIssuer, true)
			Expect(err).NotTo(HaveOccurred())
			pod := statefulSetFor(name).Spec.Template.Spec

			Expect(renderedConfig(pod)).To(HaveKeyWithValue("issuers", ConsistOf(map[string]any{
				"issuer":       testGaramIssuer,
				"keys-url":     "https://" + testGaramAddress + "/message-signing-keys",
				"keys-ca-file": "/run/sherlock/credentials/server-root.pem",
			})))

			By("the keys-ca-file inside the credential copy the agent container already mounts")
			Expect(containerOf(pod, agentContainerName).VolumeMounts).To(ContainElement(
				corev1.VolumeMount{Name: credentialsVolumeName, MountPath: agentTypeSherlock.credentialsMountPath}))
			Expect(agentTypeSherlock.credentialsMountPath).To(Equal("/run/sherlock/credentials"))
			By("the same file the adapter reads as its server root, from the same volume")
			adapter := initContainerOf(pod, adapterContainerName)
			Expect(environmentOf(adapter)).To(HaveKeyWithValue(adapterServerRootSetting,
				adapterCredentialsMountPath+"/server-root.pem"))
			Expect(adapter.VolumeMounts).To(ContainElement(HaveField("Name", credentialsVolumeName)))
		}
	})

	It("renders no issuer where the manager names none, or where the agent's Pod carries no adapter", func() {
		name := "issuer-absent"
		agentFrom(name, agentv1alpha1.DesiredSourceGaram)

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

func TestValidateIssuer_AcceptsOnlyAnHTTPSOrigin(t *testing.T) {
	for _, issuer := range []string{"https://garam.example.com", "https://garam.example.com:8443"} {
		if err := ValidateIssuer(issuer); err != nil {
			t.Errorf("ValidateIssuer(%q) = %v, want nil", issuer, err)
		}
	}
	for _, issuer := range []string{
		"", "garam.example.com", "http://garam.example.com", "https://garam.example.com/",
		"https://garam.example.com/machine", "https://garam.example.com?x=1", "https://garam.example.com?",
		"https://garam.example.com#", "https://user@garam.example.com", "https://",
	} {
		if err := ValidateIssuer(issuer); err == nil {
			t.Errorf("ValidateIssuer(%q) = nil, want an error", issuer)
		}
	}
}
