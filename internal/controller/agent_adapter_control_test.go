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

// legacySettings are the adapter's settings in the legacy, unfenced mode, and
// fencedSettings those it activates through the control service with.
var (
	legacySettings = []string{
		adapterAgentSetting, adapterMachineURLSetting, adapterGatewayURLSetting, adapterGatewayAgentSetting,
		adapterCertFileSetting, adapterKeyFileSetting, adapterServerRootSetting,
	}
	fencedSettings = []string{
		adapterAgentSetting, adapterMachineURLSetting, adapterGatewayURLSetting,
		adapterCertFileSetting, adapterKeyFileSetting, adapterServerRootSetting,
		adapterControlURLSetting, adapterControlRootSetting, adapterPlacementTokenSetting,
		adapterOutboxDirSetting,
	}
)

// reconcileAgentWithAdapterControl runs one reconcile for the named Agent, with
// this operator placing garam's adapter and a workspace, and giving the adapter
// the control service's settings where control is set.
func reconcileAgentWithAdapterControl(name string, control bool) (reconcile.Result, error) {
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
		AdapterControl:  control,
		ControlAddress:  testControlAddress,
		ControlRootFile: rootFile,
	})
}

// agentFrom creates an Agent carrying testGRN on source, and its credential.
func agentFrom(name string, source agentv1alpha1.DesiredSource) {
	GinkgoHelper()

	createSecret(credentialsSecretName(name))
	agent := newAgent(name)
	agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, Source: source}
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
	It("keeps the adapter legacy with the switch off, and gives a managed agent's adapter every control setting with it on", func() {
		By("the control: with the switch off, a managed agent's adapter is legacy")
		off := "adapter-control-off"
		agentFrom(off, agentv1alpha1.DesiredSourceControl)
		_, err := reconcileAgentWithAdapterControl(off, false)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(off).Spec.Template.Spec
		Expect(settingNames(initContainerOf(pod, adapterContainerName))).To(ConsistOf(legacySettings))
		Expect(initContainerOf(pod, adapterContainerName).VolumeMounts).
			NotTo(ContainElement(HaveField("Name", stateVolumeName)))
		Expect(pod.InitContainers).NotTo(ContainElement(HaveField("Name", outboxContainerName)))
		Expect(pod.Volumes).NotTo(ContainElement(HaveField("Name", controlRootVolumeName)))

		By("with the switch on, all ten settings, the gateway's agent dropped")
		on := "adapter-control-on"
		agentFrom(on, agentv1alpha1.DesiredSourceControl)
		_, err = reconcileAgentWithAdapterControl(on, true)
		Expect(err).NotTo(HaveOccurred())
		pod = statefulSetFor(on).Spec.Template.Spec
		adapter := initContainerOf(pod, adapterContainerName)
		Expect(settingNames(adapter)).To(ConsistOf(fencedSettings))
		settings := environmentOf(adapter)
		Expect(settings).To(HaveKeyWithValue(adapterControlURLSetting, "https://"+testControlAddress))
		Expect(settings).To(HaveKeyWithValue(adapterControlRootSetting, "/run/garam/control/root.pem"))
		Expect(settings).To(HaveKeyWithValue(adapterPlacementTokenSetting, "/run/garam/placement/token"))
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

		By("a Garam-source agent staying legacy under the same switch, since it registers no placement")
		claimed := "adapter-control-garam-source"
		agentFrom(claimed, agentv1alpha1.DesiredSourceGaram)
		_, err = reconcileAgentWithAdapterControl(claimed, true)
		Expect(err).NotTo(HaveOccurred())
		pod = statefulSetFor(claimed).Spec.Template.Spec
		Expect(settingNames(initContainerOf(pod, adapterContainerName))).To(ConsistOf(legacySettings))
		Expect(pod.InitContainers).NotTo(ContainElement(HaveField("Name", outboxContainerName)))
	})

	It("mounts the agent's outbox into the adapter alone, and nothing else of the state claim", func() {
		name := "outbox-adapter-only"
		agentFrom(name, agentv1alpha1.DesiredSourceControl)
		_, err := reconcileAgentWithAdapterControl(name, true)
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
