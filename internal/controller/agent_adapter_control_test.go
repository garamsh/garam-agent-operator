package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// testControlAddress is the control service the specs name, and testControlRoot
// the root they put in the control-trust file.
const (
	testControlAddress = "control.garam.svc:8443"
	testControlRoot    = "-----BEGIN CERTIFICATE-----\ncontrol-root\n-----END CERTIFICATE-----\n"
)

// fencedSettings are the adapter's settings, which it activates through the
// control service with.
var fencedSettings = []string{
	adapterAgentSetting, adapterMachineURLSetting, adapterGatewayURLSetting,
	adapterTokenFileSetting, adapterServerRootSetting,
	adapterControlURLSetting, adapterControlRootSetting, adapterControlTokenSetting, adapterPlacementTokenSetting,
	adapterOutboxDirSetting,
}

// reconcileAgentWithControl runs one reconcile for the named Agent, with this
// operator placing garam's adapter and a workspace, and naming the control
// service the adapter activates through.
func reconcileAgentWithControl(name string) (reconcile.Result, error) {
	GinkgoHelper()

	rootFile := filepath.Join(GinkgoT().TempDir(), "control-root.pem")
	Expect(os.WriteFile(rootFile, []byte(testControlRoot), 0o600)).To(Succeed())

	return runReconcile(name, &AgentReconciler{
		Client:          k8sClient,
		Scheme:          k8sClient.Scheme(),
		CopyImage:       testCopyImage,
		WorkspaceImage:  testWorkspaceImage,
		AdapterImage:    testAdapterImage,
		GaramAddress:    testGaramAddress,
		ControlAddress:  testControlAddress,
		ControlRootFile: rootFile,
	})
}

// agentFrom creates an Agent carrying testGRN, and its credential.
func agentFrom(name string) {
	GinkgoHelper()

	createSecret(credentialsSecretName(name))
	agent := newAgent(name)
	agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
	createAgent(agent)
}

// settingNames are the names of the variables the container is given.
func settingNames(container corev1.Container) []string {
	names := make([]string, 0, len(container.Env))
	for _, variable := range container.Env {
		names = append(names, variable.Name)
	}

	return names
}

var _ = Describe("Adapter control settings", func() {
	It("gives an agent's adapter every control setting, and builds none where no control service is named", func() {
		By("the control: with no control service named, the agent's Pod carries no adapter")
		unnamed := "adapter-control-unnamed"
		agentFrom(unnamed)
		_, err := runReconcile(unnamed, &AgentReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), CopyImage: testCopyImage,
			AdapterImage: testAdapterImage, GaramAddress: testGaramAddress,
		})
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(unnamed).Spec.Template.Spec
		Expect(pod.InitContainers).NotTo(ContainElement(HaveField("Name", adapterContainerName)))
		Expect(pod.Volumes).NotTo(ContainElement(HaveField("Name", controlRootVolumeName)))

		By("an agent's adapter, all ten settings, in the token mode")
		on := "adapter-control-on"
		agentFrom(on)
		_, err = reconcileAgentWithControl(on)
		Expect(err).NotTo(HaveOccurred())
		pod = statefulSetFor(on).Spec.Template.Spec
		adapter := initContainerOf(pod, adapterContainerName)
		Expect(settingNames(adapter)).To(ConsistOf(fencedSettings))
		settings := environmentOf(adapter)
		Expect(settings).To(HaveKeyWithValue(adapterControlURLSetting, "https://"+testControlAddress))
		Expect(settings).To(HaveKeyWithValue(adapterControlRootSetting, "/run/garam/control/root.pem"))
		Expect(settings).To(HaveKeyWithValue(adapterTokenFileSetting, "/run/garam/credentials/token"))
		Expect(settings).To(HaveKeyWithValue(adapterControlTokenSetting, "/run/garam/credentials/control-token"))
		Expect(settings).To(HaveKeyWithValue(adapterPlacementTokenSetting, "/run/garam/placement/token"))
		// garam refuses a certificate's settings beside the token file
		// (garam@afde8b8:docs/architecture/adapter.md §Token mode).
		Expect(settings).NotTo(HaveKey("GARAM_ADAPTER_TLS_CERT_FILE"))
		Expect(settings).NotTo(HaveKey("GARAM_ADAPTER_TLS_KEY_FILE"))
		Expect(settings).To(HaveKeyWithValue(adapterOutboxDirSetting, outboxMountPath))

		By("each file the settings name mounted where they name it")
		Expect(adapter.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: adapterCredentialsMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: placementVolumeName, MountPath: placementMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: controlRootVolumeName, MountPath: controlRootMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: stateVolumeName, MountPath: outboxMountPath, SubPath: "memory/outbox"}))

		By("the control root written by the config writer from the manager's own trust file")
		writer := initContainerOf(pod, configContainerName)
		Expect(environmentOf(writer)).To(HaveKeyWithValue(controlRootContentVariable, testControlRoot))
		Expect(writer.VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: controlRootVolumeName, MountPath: controlRootMountPath}))
	})

	It("mounts the agent's outbox into the adapter alone, and nothing else of the state claim", func() {
		name := "outbox-adapter-only"
		agentFrom(name)
		_, err := reconcileAgentWithControl(name)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec

		By("the control: the agent mounts the whole state claim, at its own path")
		Expect(containerOf(pod, agentContainerName).VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: stateVolumeName, MountPath: agentTypeSherlock.stateMountPath}))

		By("the adapter mounting the state claim once, at the outbox's subPath, so no path of it reaches memory.db")
		var adapterState []corev1.VolumeMount
		for _, mount := range initContainerOf(pod, adapterContainerName).VolumeMounts {
			if mount.Name == stateVolumeName {
				adapterState = append(adapterState, mount)
			}
		}
		Expect(adapterState).To(ConsistOf(HaveField("SubPath", agentTypeSherlock.outboxDir())))
		Expect(agentTypeSherlock.outboxDir()).NotTo(Equal(filepath.Dir(agentTypeSherlock.memoryFile)))

		By("no other long-running container mounting it: not the workspace")
		Expect(containerOf(pod, workspaceContainerName).VolumeMounts).
			NotTo(ContainElement(HaveField("Name", stateVolumeName)))

		By("the outbox made before the adapter starts, by an init container that runs to completion")
		outboxAt := slices.IndexFunc(pod.InitContainers, func(c corev1.Container) bool { return c.Name == outboxContainerName })
		adapterAt := slices.IndexFunc(pod.InitContainers, func(c corev1.Container) bool { return c.Name == adapterContainerName })
		Expect(outboxAt).To(SatisfyAll(BeNumerically(">=", 0), BeNumerically("<", adapterAt)))
		Expect(pod.InitContainers[outboxAt].RestartPolicy).To(BeNil())
		Expect(pod.InitContainers[outboxAt].Command).
			To(Equal(makeOutboxCommand(outboxStateMountPath + "/" + agentTypeSherlock.outboxDir())))

		By("the control root mounted into the adapter and its writer alone")
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if container.Name == adapterContainerName || container.Name == configContainerName {
				continue
			}
			Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", controlRootVolumeName)), container.Name)
		}
	})

	It("makes the outbox at its mode as the user that runs it where it is absent, and touches nothing where it exists", func() {
		By("the control: an absent outbox is made, at 0770, owned by the user running the script")
		fresh := GinkgoT().TempDir()
		outbox := filepath.Join(fresh, agentTypeSherlock.outboxDir())
		runOutboxCommand(outbox)
		info, err := os.Stat(outbox)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o770)))
		Expect(info.Sys().(*syscall.Stat_t).Uid).To(BeEquivalentTo(os.Getuid()))

		By("an outbox that exists, with an entry and a store beside it, left as it was")
		existing := GinkgoT().TempDir()
		outbox = filepath.Join(existing, agentTypeSherlock.outboxDir())
		Expect(os.MkdirAll(outbox, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(outbox, "entry.json"), []byte("{}"), 0o600)).To(Succeed())
		store := filepath.Join(existing, agentTypeSherlock.memoryFile)
		Expect(os.WriteFile(store, []byte("store"), 0o600)).To(Succeed())
		runOutboxCommand(outbox)
		info, err = os.Stat(outbox)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		Expect(filepath.Join(outbox, "entry.json")).To(BeAnExistingFile())
		Expect(os.ReadFile(store)).To(Equal([]byte("store")))
	})
})

// runOutboxCommand runs the outbox init container's script for dir, with
// /bin/sh, as it runs in the copy image.
func runOutboxCommand(dir string) {
	GinkgoHelper()

	command := makeOutboxCommand(dir)
	output, err := exec.Command(command[0], command[1:]...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(output))
}
