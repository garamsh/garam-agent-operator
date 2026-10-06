package controller

import (
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// testPinnedTool is the tool the specs declare a pin for. It is sherlock's one
// required tool, so a declaration leaving it out is an agent that cannot start
// whatever else the set names.
const testPinnedTool = "message_send"

// testToolPin is a pin the way an operator writes one. It is opaque to this
// operator, which is why no spec asserts anything about its shape.
const testToolPin = "sha256:aa"

// testEgo is an ego the way an organisation writes one.
const testEgo = "We are the platform team. We answer in plain language."

// testSecondTool and testSecondPin are a second tool in a declaration, which is
// what says a set is carried whole rather than one tool at a time.
const (
	testSecondTool = "files"
	testSecondPin  = "sha256:bb"
)

var _ = Describe("Agent workload", func() {
	It("builds the StatefulSet the Agent describes, and owns it", func() {
		name := "builds-its-workload"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		createAgent(agent)

		result, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))

		workload := statefulSetFor(name)

		By("owning it, so that deleting the Agent takes it too")
		Expect(workload.OwnerReferences).To(HaveLen(1))
		Expect(workload.OwnerReferences[0].UID).To(Equal(agent.UID))
		Expect(workload.OwnerReferences[0].Controller).To(HaveValue(BeTrue()))

		By("running one replica of the Agent's image with the Agent's resources")
		Expect(workload.Spec.Replicas).To(HaveValue(BeEquivalentTo(1)))
		Expect(workload.Spec.Template.Spec.Containers).To(HaveLen(1))
		container := workload.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal(agent.Spec.Image))
		Expect(container.Resources.Requests.Memory().String()).To(Equal("256Mi"))

		By("putting the credential nowhere in the environment, which carries the memory store's path alone")
		Expect(container.Env).To(ConsistOf(corev1.EnvVar{Name: agentTypeSherlock.memoryPathVariable, Value: agentTypeSherlock.memoryPath()}))
		Expect(container.EnvFrom).To(BeEmpty())

		By("claiming the Agent's storage size for the volume it keeps state on")
		Expect(workload.Spec.VolumeClaimTemplates).To(HaveLen(2))
		claim := workload.Spec.VolumeClaimTemplates[0]
		Expect(claim.Name).To(Equal(stateVolumeName))
		Expect(claim.Spec.Resources.Requests.Storage().String()).To(Equal("1Gi"))
		Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name:      claim.Name,
			MountPath: agentTypeSherlock.stateMountPath,
		}))
	})

	It("writes the StatefulSet no second time when the Agent has not changed", func() {
		name := "writes-once"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		created := statefulSetFor(name).ResourceVersion

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).ResourceVersion).To(Equal(created))
	})

	It("brings the StatefulSet back to the image the Agent now names", func() {
		name := "follows-the-image"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		created := statefulSetFor(name).ResourceVersion

		edited := readAgent(name)
		edited.Spec.Image = "example.com/sherlock:v0.2.0"
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		workload := statefulSetFor(name)
		Expect(workload.Spec.Template.Spec.Containers[0].Image).To(Equal("example.com/sherlock:v0.2.0"))
		Expect(workload.ResourceVersion).NotTo(Equal(created))
	})

	It("leaves the claim alone when the Agent's storage size changes, which a StatefulSet refuses", func() {
		name := "keeps-its-claim"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		edited := readAgent(name)
		edited.Spec.StorageSize = resource.MustParse("2Gi")
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String()).
			To(Equal("1Gi"))
	})

	It("puts the Agent's storage class on the claim, and leaves it unset when the Agent names none", func() {
		By("reconciling an Agent that names no storage class")
		unset := "cluster-default-class"
		createSecret(credentialsSecretName(unset))
		createAgent(newAgent(unset))

		_, err := reconcileAgent(unset)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(unset).Spec.VolumeClaimTemplates[0].Spec.StorageClassName).To(BeNil())

		By("reconciling an Agent that differs only in naming one")
		named := "named-storage-class"
		createSecret(credentialsSecretName(named))
		agent := newAgent(named)
		agent.Spec.StorageClassName = ptr.To("fast")
		createAgent(agent)

		_, err = reconcileAgent(named)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(named).Spec.VolumeClaimTemplates[0].Spec.StorageClassName).
			To(HaveValue(Equal("fast")))
	})

	It("delivers the credential from a volume the kubelet does not write, owned by the user that reads it", func() {
		name := "owns-its-credential"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec

		By("running the whole Pod as one user, which is what leaves the copy owned by its reader")
		Expect(pod.SecurityContext).NotTo(BeNil())
		Expect(pod.SecurityContext.RunAsUser).To(HaveValue(BeEquivalentTo(65532)))
		Expect(pod.SecurityContext.FSGroup).To(HaveValue(BeEquivalentTo(65532)))

		By("projecting the Secret group-readable, which is what the init container reads it by")
		projected := volumeNamed(pod, credentialsSecretVolumeName)
		Expect(projected.Secret).NotTo(BeNil())
		Expect(projected.Secret.SecretName).To(Equal(agent.Spec.CredentialsSecretName))
		Expect(projected.Secret.DefaultMode).To(HaveValue(BeEquivalentTo(0o440)))

		By("copying it into a memory-backed volume, so the copy never reaches the node's disk")
		copied := volumeNamed(pod, credentialsVolumeName)
		Expect(copied.EmptyDir).NotTo(BeNil())
		Expect(copied.EmptyDir.Medium).To(Equal(corev1.StorageMediumMemory))
		Expect(copied.EmptyDir.SizeLimit).NotTo(BeNil())

		By("making that copy before the agent starts, at a mode no group or other user can reach")
		Expect(pod.InitContainers).To(HaveLen(1))
		credentials := pod.InitContainers[0]
		Expect(credentials.Name).To(Equal(credentialsContainerName))
		Expect(credentials.Image).To(Equal(testCopyImage))
		script := strings.Join(credentials.Command, " ")
		Expect(script).To(ContainSubstring("install -m 0600"))
		Expect(script).To(ContainSubstring(agentTypeSherlock.credentialsSecretMountPath + "/*"))
		Expect(script).To(ContainSubstring(agentTypeSherlock.credentialsMountPath + "/"))
		Expect(credentials.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsSecretVolumeName, MountPath: agentTypeSherlock.credentialsSecretMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: agentTypeSherlock.credentialsMountPath},
		))

		By("letting neither container name a user of its own, which would leave the copy owned by somebody else")
		Expect(credentials.SecurityContext.RunAsUser).To(BeNil())
		Expect(pod.Containers).To(HaveLen(1))
		Expect(pod.Containers[0].SecurityContext.RunAsUser).To(BeNil())

		By("giving the agent the copy and not the projection")
		Expect(pod.Containers[0].VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: agentTypeSherlock.credentialsMountPath},
			corev1.VolumeMount{Name: stateVolumeName, MountPath: agentTypeSherlock.stateMountPath},
		))
	})

	It("builds a Pod a namespace enforcing PodSecurity restricted admits, and would not without its security context", func() {
		name := "satisfies-restricted"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		namespace := restrictedNamespace("psa-" + name)

		By("creating the Pod the StatefulSet describes, which the API server admits")
		Expect(k8sClient.Create(ctx, podOf(statefulSetFor(name), namespace))).To(Succeed())

		By("creating the same Pod without the four fields, which it refuses")
		refused := podOf(statefulSetFor(name), namespace)
		refused.Name += "-unhardened"
		refused.Spec.SecurityContext.RunAsNonRoot = nil
		refused.Spec.SecurityContext.SeccompProfile = nil
		for i := range refused.Spec.InitContainers {
			refused.Spec.InitContainers[i].SecurityContext = nil
		}
		for i := range refused.Spec.Containers {
			refused.Spec.Containers[i].SecurityContext = nil
		}
		Expect(k8sClient.Create(ctx, refused)).
			To(MatchError(ContainSubstring(`violates PodSecurity "restricted:latest"`)))
	})

	It("pulls both images at every start, so that no node runs one it cached", func() {
		name := "pulls-what-it-runs"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
		Expect(pod.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
	})

	It("builds a Pod carrying no tool tree, because the agent's image carries its own tools", func() {
		name := "carries-no-tool-tree"
		createSecret(credentialsSecretName(name))
		// Every part this operator can build is present, so no container the
		// Pod could carry escapes the check below.
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		createAgent(agent)

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.Containers).To(HaveLen(2))
		Expect(pod.InitContainers).To(HaveLen(2))
		Expect(toolTreeIn(pod)).To(BeEmpty())
	})

	It("takes the tool tree back out of a workload an operator naming a tools image built", func() {
		name := "drops-its-tool-tree"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		By("writing onto the StatefulSet the volume, mount and variable that operator wrote")
		workload := statefulSetFor(name)
		pod := &workload.Spec.Template.Spec
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: retiredToolsVolume,
			VolumeSource: corev1.VolumeSource{
				Image: &corev1.ImageVolumeSource{Reference: "example.com/tools:v0.1.0", PullPolicy: corev1.PullAlways},
			},
		})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: retiredToolsVolume, MountPath: "/opt/sherlock/tools", ReadOnly: true})
		pod.Containers[0].Env = append(pod.Containers[0].Env,
			corev1.EnvVar{Name: retiredToolsDirVariable, Value: "/opt/sherlock/tools"})
		Expect(k8sClient.Update(ctx, workload)).To(Succeed())

		By("finding all three on it, which is what leaves the check below something to find")
		Expect(toolTreeIn(statefulSetFor(name).Spec.Template.Spec)).To(HaveLen(3))

		By("reconciling, which leaves none of them")
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(toolTreeIn(statefulSetFor(name).Spec.Template.Spec)).To(BeEmpty())
	})

	It("builds the workspace this operator names, and points both ends of the link at one address", func() {
		name := "carries-a-workspace"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec

		By("running it beside the agent, which stays the container an Agent's spec describes")
		Expect(pod.Containers).To(HaveLen(2))
		Expect(pod.Containers[0].Name).To(Equal(agentContainerName))
		workspace := containerOf(pod, workspaceContainerName)
		Expect(workspace.Image).To(Equal(testWorkspaceImage))
		Expect(workspace.ImagePullPolicy).To(Equal(corev1.PullAlways))

		By("telling the workspace where to listen and the agent the same place to dial")
		listen := environmentOf(workspace)
		Expect(listen[agentTypeSherlock.listenAddressVariable]).To(Equal(agentTypeSherlock.workspaceAddress))
		Expect(environmentOf(pod.Containers[0])[agentTypeSherlock.workspaceAddressVariable]).To(Equal(listen[agentTypeSherlock.listenAddressVariable]))

		By("giving it a directory on a volume of its own, at the path the agent holds its state at")
		Expect(workspace.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: workspaceVolumeName, MountPath: agentTypeSherlock.stateMountPath}))
		Expect(listen[agentTypeSherlock.workspaceDirVariable]).To(HavePrefix(agentTypeSherlock.stateMountPath + "/"))

		By("leaving what its image runs alone, and mounting it no credential it does not read")
		Expect(workspace.Command).To(BeEmpty())
		Expect(workspace.Args).To(BeEmpty())
		Expect(workspace.VolumeMounts).NotTo(ContainElement(HaveField("Name", credentialsVolumeName)))
	})

	It("puts the agent's memory store and its outbox on the state volume, which the workspace does not mount", func() {
		name := "keeps-memory-on-its-volume"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		agentContainer := containerOf(pod, agentContainerName)
		volume := agentTypeSherlock.stateMountPath + "/"

		By("pointing the agent at an absolute path under the mount of the volume its state is claimed on")
		Expect(agentContainer.VolumeMounts).To(ContainElement(
			corev1.VolumeMount{Name: stateVolumeName, MountPath: agentTypeSherlock.stateMountPath}))
		Expect(environmentOf(agentContainer)).To(HaveKey(agentTypeSherlock.memoryPathVariable))
		memoryPath := environmentOf(agentContainer)[agentTypeSherlock.memoryPathVariable]
		Expect(memoryPath).To(HavePrefix(volume))

		By("giving the store a directory of its own, so the outbox derived beside it lands in the same subtree")
		memoryDir := path.Dir(memoryPath) + "/"
		Expect(memoryDir).To(HavePrefix(volume))
		Expect(memoryDir).NotTo(Equal(volume))

		By("mounting the state volume into the agent alone, so the memory path resolves to nothing in the workspace")
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if container.Name == agentContainerName {
				continue
			}
			Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", stateVolumeName)), container.Name)
		}
		workspace := containerOf(pod, workspaceContainerName)
		Expect(workspace.VolumeMounts).To(ConsistOf(HaveField("Name", workspaceVolumeName)))
	})

	It("tells the workspace to run exec children as the user the Pod names, which is what lets it run any", func() {
		name := "agrees-on-one-uid"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		workspace := containerOf(pod, workspaceContainerName)

		By("naming no user of its own, so the Pod's is the user it runs as")
		Expect(workspace.SecurityContext.RunAsUser).To(BeNil())
		Expect(pod.SecurityContext.RunAsUser).NotTo(BeNil())

		By("stating that same user as the account exec children run under, which sherlock refuses to guess")
		Expect(environmentOf(workspace)[agentTypeSherlock.execUserVariable]).
			To(Equal(strconv.FormatInt(*pod.SecurityContext.RunAsUser, 10)))
	})

	It("builds the Pod it built before a workspace existed where this operator names no workspace image", func() {
		shared := "shared-workspace-credentials"
		createSecret(shared)

		By("reconciling an Agent while this operator names a workspace image")
		named := "workspace-image-named"
		withWorkspace := newAgent(named)
		withWorkspace.Spec.CredentialsSecretName = shared
		// One identity for both, so that the agent ID the two are started under
		// is not a difference between them.
		withWorkspace.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(withWorkspace)
		_, err := reconcileAgentWithWorkspace(named)
		Expect(err).NotTo(HaveOccurred())

		By("reconciling an Agent identical to it while this operator names none")
		unnamed := "workspace-image-unset"
		withoutWorkspaceImage := newAgent(unnamed)
		withoutWorkspaceImage.Spec.CredentialsSecretName = shared
		withoutWorkspaceImage.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(withoutWorkspaceImage)
		_, err = reconcileAgent(unnamed)
		Expect(err).NotTo(HaveOccurred())

		unset := statefulSetFor(unnamed).Spec.Template.Spec

		By("carrying no second container and telling the agent to dial nothing")
		Expect(unset.Containers).To(HaveLen(1))
		Expect(unset.Containers[0].Env).To(ConsistOf(corev1.EnvVar{Name: agentTypeSherlock.memoryPathVariable, Value: agentTypeSherlock.memoryPath()}))

		set := statefulSetFor(named).Spec.Template.Spec

		By("differing from the Pod built with one, which is what leaves the comparison below something to isolate")
		Expect(set).NotTo(Equal(unset))

		By("differing from it in those two places and in nothing else")
		Expect(withoutWorkspace(set)).To(Equal(unset))
	})

	It("takes the workspace back out when this operator stops naming an image for it", func() {
		name := "drops-its-workspace"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.Containers).To(HaveLen(2))

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.Containers).To(HaveLen(1))
		Expect(pod.Containers[0].Name).To(Equal(agentContainerName))
		Expect(pod.Containers[0].Env).To(ConsistOf(corev1.EnvVar{Name: agentTypeSherlock.memoryPathVariable, Value: agentTypeSherlock.memoryPath()}))
	})

	It("builds a Pod carrying the workspace that a namespace enforcing PodSecurity restricted admits", func() {
		name := "workspace-satisfies-restricted"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithWorkspace(name)
		Expect(err).NotTo(HaveOccurred())
		namespace := restrictedNamespace("psa-" + name)

		By("creating the Pod the StatefulSet describes, which carries the workspace and which is admitted")
		admitted := podOf(statefulSetFor(name), namespace)
		Expect(admitted.Spec.Containers).To(HaveLen(2))
		Expect(k8sClient.Create(ctx, admitted)).To(Succeed())

		By("creating the same Pod with the capability sherlock needs only on the root path, which it refuses")
		refused := podOf(statefulSetFor(name), namespace)
		refused.Name += "-privileged"
		at := slices.IndexFunc(refused.Spec.Containers, func(container corev1.Container) bool {
			return container.Name == workspaceContainerName
		})
		Expect(at).To(BeNumerically(">=", 0))
		refused.Spec.Containers[at].SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		Expect(k8sClient.Create(ctx, refused)).
			To(MatchError(ContainSubstring(`violates PodSecurity "restricted:latest"`)))
	})

	It("places garam's adapter as a native sidecar that reaches garam as the agent and its gateway on loopback", func() {
		name := "places-the-adapter"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)

		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec
		adapter := initContainerOf(pod, adapterContainerName)

		By("running garam's adapter subcommand as a sidecar that keeps running, answering no probe")
		Expect(adapter.Image).To(Equal(testAdapterImage))
		Expect(adapter.ImagePullPolicy).To(Equal(corev1.PullAlways))
		Expect(adapter.RestartPolicy).To(HaveValue(Equal(corev1.ContainerRestartPolicyAlways)))
		Expect(adapter.Command).To(BeEmpty())
		Expect(adapter.Args).To(Equal([]string{"adapter"}))
		Expect(adapter.LivenessProbe).To(BeNil())
		Expect(adapter.ReadinessProbe).To(BeNil())
		Expect(adapter.StartupProbe).To(BeNil())

		By("starting after the credential it reads has been copied")
		names := make([]string, 0, len(pod.InitContainers))
		for _, initContainer := range pod.InitContainers {
			names = append(names, initContainer.Name)
		}
		Expect(slices.Index(names, adapterContainerName)).
			To(BeNumerically(">", slices.Index(names, credentialsContainerName)))

		By("configuring it as the agent, against garam's listener and the agent's gateway")
		gatewayAddress := agentTypeSherlock.gatewayAddress
		Expect(environmentOf(adapter)).To(Equal(map[string]string{
			adapterAgentSetting:        testGRN,
			adapterMachineURLSetting:   "https://" + testGaramAddress,
			adapterGatewayURLSetting:   "http://" + gatewayAddress,
			adapterGatewayAgentSetting: testGRN,
			adapterCertFileSetting:     adapterCredentialsMountPath + "/certificate.pem",
			adapterKeyFileSetting:      adapterCredentialsMountPath + "/key.pem",
			adapterServerRootSetting:   adapterCredentialsMountPath + "/server-root.pem",
		}))

		By("mounting the agent's credential copy and the placement token's copy read-only, and nothing else")
		Expect(adapter.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: adapterCredentialsMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: placementVolumeName, MountPath: placementMountPath, ReadOnly: true},
		))

		By("telling the agent's gateway to listen where the adapter dials, and mounting nothing new on the agent")
		agentContainer := containerOf(pod, agentContainerName)
		Expect(environmentOf(agentContainer)).To(HaveKeyWithValue(agentTypeSherlock.listenAddressVariable, gatewayAddress))
		for _, mount := range agentContainer.VolumeMounts {
			Expect(mount.MountPath).NotTo(Equal(adapterCredentialsMountPath))
		}
	})

	It("builds the Pod it built before an adapter existed where this operator names no adapter image", func() {
		shared := "shared-adapter-credentials"
		createSecret(shared)

		By("reconciling an Agent while this operator names an adapter image")
		named := "adapter-image-named"
		withAdapter := newAgent(named)
		withAdapter.Spec.CredentialsSecretName = shared
		withAdapter.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(withAdapter)
		_, err := reconcileAgentWithAdapter(named)
		Expect(err).NotTo(HaveOccurred())

		By("reconciling an Agent identical to it while this operator names none")
		unnamed := "adapter-image-unset"
		withoutAdapterImage := newAgent(unnamed)
		withoutAdapterImage.Spec.CredentialsSecretName = shared
		withoutAdapterImage.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(withoutAdapterImage)
		_, err = reconcileAgent(unnamed)
		Expect(err).NotTo(HaveOccurred())

		set := statefulSetFor(named).Spec.Template.Spec
		unset := statefulSetFor(unnamed).Spec.Template.Spec

		By("differing from the Pod built with one, which is what leaves the comparison below something to isolate")
		Expect(set).NotTo(Equal(unset))

		By("differing from it in the sidecar and the gateway's address and in nothing else")
		Expect(withoutAdapter(set)).To(Equal(unset))
	})

	It("places no adapter beside an Agent with no GRN, or where no garam listener is configured", func() {
		name := "adapter-without-grn"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		By("reconciling an Agent a user wrote while this operator names an adapter image")
		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(environmentOf(containerOf(pod, agentContainerName))).
			NotTo(HaveKey(agentTypeSherlock.listenAddressVariable))

		By("reconciling an Agent with a GRN while this operator names an image and no garam listener")
		withGRN := "adapter-without-listener"
		createSecret(credentialsSecretName(withGRN))
		agent := newAgent(withGRN)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)
		_, err = runReconcile(withGRN, &AgentReconciler{
			Client: k8sClient, Scheme: k8sClient.Scheme(), CopyImage: testCopyImage, AdapterImage: testAdapterImage,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(withGRN).Spec.Template.Spec.InitContainers).To(HaveLen(1))
	})

	It("takes the adapter back out when this operator stops naming an image for it", func() {
		name := "drops-the-adapter"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)

		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		// The credential's copy, the config writer the reply instruction brings,
		// and the adapter.
		Expect(statefulSetFor(name).Spec.Template.Spec.InitContainers).To(HaveLen(3))

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(pod.InitContainers[0].Name).To(Equal(credentialsContainerName))
		Expect(environmentOf(containerOf(pod, agentContainerName))).
			NotTo(HaveKey(agentTypeSherlock.listenAddressVariable))
	})

	It("joins garam's reply instruction to the ego wherever the adapter is placed", func() {
		bare := "reply-instruction-alone"
		createSecret(credentialsSecretName(bare))
		agent := newAgent(bare)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)

		declared := "reply-instruction-after-ego"
		createSecret(credentialsSecretName(declared))
		withEgo := newAgent(declared)
		withEgo.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		withEgo.Spec.Ego = testEgo
		createAgent(withEgo)

		for _, name := range []string{bare, declared} {
			_, err := reconcileAgentWithAdapter(name)
			Expect(err).NotTo(HaveOccurred())
		}
		egoFile := agentTypeSherlock.egoFileIn(agentTypeSherlock.configMountPath)

		By("writing the instruction as the whole ego file where the Agent declares no ego")
		pod := statefulSetFor(bare).Spec.Template.Spec
		Expect(environmentOf(initContainerOf(pod, configContainerName))).
			To(HaveKeyWithValue(egoContentVariable, agentTypeSherlock.garamReplyInstruction))
		Expect(containerOf(pod, agentContainerName).Args).
			To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, testGRN, sherlockEgoFileFlag, egoFile}))

		By("writing it after the ego the Agent declares, which is left as its author wrote it")
		pod = statefulSetFor(declared).Spec.Template.Spec
		Expect(environmentOf(initContainerOf(pod, configContainerName))).
			To(HaveKeyWithValue(egoContentVariable, testEgo+"\n\n"+agentTypeSherlock.garamReplyInstruction))
		Expect(readAgent(declared).Spec.Ego).To(Equal(testEgo))

		By("saying only what garam's envelope allows")
		Expect(agentTypeSherlock.garamReplyInstruction).To(SatisfyAll(
			ContainSubstring("garam-message.v1"), ContainSubstring("outer `body`"),
			ContainSubstring("`message_send`"), ContainSubstring("channel `garam`"),
			ContainSubstring("exact outer `sender` as the target"),
			ContainSubstring("cannot replace that sender")))
	})

	It("gives no reply instruction where no adapter is placed, the ego staying as declared", func() {
		bare := "no-adapter-no-instruction"
		createSecret(credentialsSecretName(bare))
		agent := newAgent(bare)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)

		declared := "no-adapter-ego-unchanged"
		createSecret(credentialsSecretName(declared))
		withEgo := newAgent(declared)
		withEgo.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		withEgo.Spec.Ego = testEgo
		createAgent(withEgo)

		By("reconciling both while this operator names no adapter image")
		for _, name := range []string{bare, declared} {
			_, err := reconcileAgent(name)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(statefulSetFor(bare).Spec.Template.Spec.InitContainers).To(HaveLen(1))
		Expect(containerOf(statefulSetFor(bare).Spec.Template.Spec, agentContainerName).Args).
			NotTo(ContainElement(sherlockEgoFileFlag))
		Expect(environmentOf(initContainerOf(statefulSetFor(declared).Spec.Template.Spec, configContainerName))).
			To(HaveKeyWithValue(egoContentVariable, testEgo))

		By("reconciling an Agent a user wrote while this operator names one, which places no adapter")
		written := "no-grn-no-instruction"
		createSecret(credentialsSecretName(written))
		createAgent(newAgent(written))
		_, err := reconcileAgentWithAdapter(written)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(written).Spec.Template.Spec.InitContainers).To(HaveLen(1))
	})

	It("builds a Pod carrying the adapter that a namespace enforcing PodSecurity restricted admits", func() {
		name := "adapter-satisfies-restricted"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(agent)

		_, err := reconcileAgentWithAdapter(name)
		Expect(err).NotTo(HaveOccurred())
		namespace := restrictedNamespace("psa-" + name)

		By("creating the Pod the StatefulSet describes, which carries the adapter and which is admitted")
		admitted := podOf(statefulSetFor(name), namespace)
		Expect(admitted.Spec.InitContainers).To(HaveLen(3))
		Expect(k8sClient.Create(ctx, admitted)).To(Succeed())

		By("creating the same Pod with the adapter allowed to escalate its privileges, which it refuses")
		refused := podOf(statefulSetFor(name), namespace)
		refused.Name += "-escalating"
		at := slices.IndexFunc(refused.Spec.InitContainers, func(container corev1.Container) bool {
			return container.Name == adapterContainerName
		})
		Expect(at).To(BeNumerically(">=", 0))
		refused.Spec.InitContainers[at].SecurityContext.AllowPrivilegeEscalation = ptr.To(true)
		Expect(k8sClient.Create(ctx, refused)).
			To(MatchError(ContainSubstring(`violates PodSecurity "restricted:latest"`)))
	})

	It("writes the tool set an Agent declares into a file the agent reads, and points the agent at it", func() {
		name := "declares-a-tool-set"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin, testSecondTool: testSecondPin}
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec

		By("writing it into a volume of its own, which is not the memory a credential's copy takes")
		written := volumeNamed(pod, configVolumeName)
		Expect(written.EmptyDir).NotTo(BeNil())
		Expect(written.EmptyDir.Medium).To(BeEmpty())

		By("writing it before the agent starts, at a mode sherlock does not refuse")
		config := initContainerOf(pod, configContainerName)
		Expect(config.Image).To(Equal(testCopyImage))
		Expect(config.ImagePullPolicy).To(Equal(corev1.PullAlways))
		script := strings.Join(config.Command, " ")
		Expect(script).To(ContainSubstring("umask 077"))
		Expect(script).To(ContainSubstring(agentTypeSherlock.configFileIn(agentTypeSherlock.configMountPath)))
		Expect(config.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: configVolumeName, MountPath: agentTypeSherlock.configMountPath}))

		By("carrying the file's text to that container and to nothing the agent spawns")
		Expect(environmentOf(config)).To(HaveKeyWithValue(configContentVariable,
			SatisfyAll(ContainSubstring(testPinnedTool+": "+testToolPin), ContainSubstring(testSecondTool+": "+testSecondPin))))
		agentContainer := containerOf(pod, agentContainerName)
		Expect(environmentOf(agentContainer)).NotTo(HaveKey(configContentVariable))

		By("giving the agent the file read-only and the directory it resolves one from")
		Expect(agentContainer.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: configVolumeName, MountPath: agentTypeSherlock.configMountPath, ReadOnly: true}))
		Expect(environmentOf(agentContainer)).To(HaveKeyWithValue(agentTypeSherlock.configHomeVariable, agentTypeSherlock.configMountPath))
	})

	It("writes garam's reply instruction into an instructions file, leaving the ego the Agent's, once that file is rendered", func() {
		agentWith := func(name, ego string) {
			createSecret(credentialsSecretName(name))
			agent := newAgent(name)
			agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
			agent.Spec.Ego = ego
			createAgent(agent)
		}
		egoFile := agentTypeSherlock.egoFileIn(agentTypeSherlock.configMountPath)
		instructionsFile := agentTypeSherlock.instructionsFileIn(agentTypeSherlock.configMountPath)

		By("the control: with the switch off, the instruction is joined to the declared ego and no instructions file is named")
		joined := "instructions-switch-off"
		agentWith(joined, testEgo)
		_, err := reconcileAgentWithAdapter(joined)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(joined).Spec.Template.Spec
		Expect(environmentOf(initContainerOf(pod, configContainerName))).
			To(HaveKeyWithValue(egoContentVariable, testEgo+"\n\n"+agentTypeSherlock.garamReplyInstruction))
		Expect(environmentOf(initContainerOf(pod, configContainerName))).NotTo(HaveKey(instructionsContentVariable))
		Expect(containerOf(pod, agentContainerName).Args).NotTo(ContainElement(sherlockInstructionsFlag))

		By("with the switch on, the instruction is the instructions file, and the ego exactly as declared")
		declared := "instructions-beside-ego"
		agentWith(declared, testEgo)
		_, err = reconcileAgentWithInstructions(declared)
		Expect(err).NotTo(HaveOccurred())
		pod = statefulSetFor(declared).Spec.Template.Spec
		written := environmentOf(initContainerOf(pod, configContainerName))
		Expect(written).To(HaveKeyWithValue(instructionsContentVariable, agentTypeSherlock.garamReplyInstruction))
		Expect(written).To(HaveKeyWithValue(egoContentVariable, testEgo))
		Expect(containerOf(pod, agentContainerName).Args).To(Equal([]string{sherlockAgentCommand,
			sherlockAgentIDFlag, testGRN, sherlockEgoFileFlag, egoFile, sherlockInstructionsFlag, instructionsFile}))

		By("with the switch on and no declared ego, no ego file, so the image's default ego is kept")
		bare := "instructions-default-ego"
		agentWith(bare, "")
		_, err = reconcileAgentWithInstructions(bare)
		Expect(err).NotTo(HaveOccurred())
		pod = statefulSetFor(bare).Spec.Template.Spec
		written = environmentOf(initContainerOf(pod, configContainerName))
		Expect(written).To(HaveKeyWithValue(instructionsContentVariable, agentTypeSherlock.garamReplyInstruction))
		Expect(written).NotTo(HaveKey(egoContentVariable))
		Expect(containerOf(pod, agentContainerName).Args).To(Equal([]string{sherlockAgentCommand,
			sherlockAgentIDFlag, testGRN, sherlockInstructionsFlag, instructionsFile}))

		By("mounting the file read-only into the agent and into no other container that runs beside it")
		Expect(containerOf(pod, agentContainerName).VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: configVolumeName, MountPath: agentTypeSherlock.configMountPath, ReadOnly: true}))
		for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
			if container.Name == agentContainerName || container.Name == configContainerName {
				continue
			}
			Expect(container.VolumeMounts).NotTo(ContainElement(HaveField("Name", configVolumeName)), container.Name)
		}
	})

	It("renders no instructions file where no adapter is placed, whether or not the switch is on", func() {
		By("the control: an Agent with an identity, with the adapter placed and the switch on")
		placed := "instructions-adapter-placed"
		createSecret(credentialsSecretName(placed))
		withAdapter := newAgent(placed)
		withAdapter.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		createAgent(withAdapter)
		_, err := reconcileAgentWithInstructions(placed)
		Expect(err).NotTo(HaveOccurred())
		Expect(containerOf(statefulSetFor(placed).Spec.Template.Spec, agentContainerName).Args).
			To(ContainElement(sherlockInstructionsFlag))

		By("an Agent a user wrote, with no identity, so no adapter, under the same switch")
		written := "instructions-no-adapter"
		createSecret(credentialsSecretName(written))
		withEgo := newAgent(written)
		withEgo.Spec.Ego = testEgo
		createAgent(withEgo)
		_, err = reconcileAgentWithInstructions(written)
		Expect(err).NotTo(HaveOccurred())
		pod := statefulSetFor(written).Spec.Template.Spec
		Expect(environmentOf(initContainerOf(pod, configContainerName))).NotTo(HaveKey(instructionsContentVariable))
		Expect(environmentOf(initContainerOf(pod, configContainerName))).To(HaveKeyWithValue(egoContentVariable, testEgo))
		Expect(containerOf(pod, agentContainerName).Args).NotTo(ContainElement(sherlockInstructionsFlag))
	})

	It("writes the instructions file beside the ego, at the mode the config file takes", func() {
		dir := GinkgoT().TempDir()
		command := writeConfigCommand(dir, agentTypeSherlock, true, true, false)
		run := exec.Command(command[0], command[1:]...)
		run.Dir = dir
		run.Env = append(os.Environ(), configContentVariable+"=tools: {}\n", egoContentVariable+"="+testEgo,
			instructionsContentVariable+"="+agentTypeSherlock.garamReplyInstruction)
		output, err := run.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))

		By("the control: the ego file, holding the ego alone")
		ego, err := os.ReadFile(agentTypeSherlock.egoFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(ego)).To(Equal(testEgo))

		By("the instructions file, holding the instruction exactly")
		instructions, err := os.ReadFile(agentTypeSherlock.instructionsFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(instructions)).To(Equal(agentTypeSherlock.garamReplyInstruction))
		info, err := os.Stat(agentTypeSherlock.instructionsFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("writes a config file only its owner can read, out of text no pin can turn into a command", func() {
		dir := GinkgoT().TempDir()
		// A pin is a string this operator does not read and a console user
		// types. This one closes the quoting a command would carry it in.
		pins := map[string]string{
			testSecondTool: `sha256:bb" ; touch escaped ; echo "`,
			testPinnedTool: testToolPin,
		}
		file, err := agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Tools: agentv1alpha1.ToolSet{Pins: pins}})
		Expect(err).NotTo(HaveOccurred())

		command := writeConfigCommand(dir, agentTypeSherlock, false, false, false)
		run := exec.Command(command[0], command[1:]...)
		run.Dir = dir
		run.Env = append(os.Environ(), configContentVariable+"="+file)
		output, err := run.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))

		By("leaving a file sherlock's owner-only rule accepts")
		info, err := os.Stat(agentTypeSherlock.configFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		By("landing every pin in it exactly as it was declared")
		content, err := os.ReadFile(agentTypeSherlock.configFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		written := sherlockConfig{}
		Expect(yaml.Unmarshal(content, &written)).To(Succeed())
		Expect(written.Tools.Pins).To(Equal(pins))
	})

	It("renders one declaration as one text, whatever order the declaration is held in", func() {
		// Three tools, held in an order that is not the sorted one. A Go map is
		// iterated in no fixed order, so a render reading it directly would put
		// the same declaration in the workload differently from pass to pass,
		// and every pass would rewrite the StatefulSet.
		pins := map[string]string{"web_fetch": "sha256:cc", testPinnedTool: testToolPin, testSecondTool: testSecondPin}

		file, err := agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Tools: agentv1alpha1.ToolSet{Pins: pins}})
		Expect(err).NotTo(HaveOccurred())

		Expect(file).To(Equal("tools:\n  pins:\n" +
			"    " + testSecondTool + ": " + testSecondPin + "\n" +
			"    " + testPinnedTool + ": " + testToolPin + "\n" +
			"    web_fetch: sha256:cc\n"))
	})

	It("builds the Pod it built before a tool set could be declared where an Agent declares none", func() {
		name := "declares-no-tools"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		withoutPins := statefulSetFor(name).Spec.Template.Spec

		edited := readAgent(name)
		edited.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		withPins := statefulSetFor(name).Spec.Template.Spec

		// The control: declaring a tool set moves the Pod, so the comparison
		// below is what the declaration adds and not two reads of one state.
		Expect(withPins).NotTo(Equal(withoutPins))
		Expect(withoutToolPins(withPins)).To(Equal(withoutPins))
	})

	It("takes the config file back out when an Agent stops declaring a tool set", func() {
		name := "stops-declaring-tools"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.InitContainers).To(HaveLen(2))

		edited := readAgent(name)
		edited.Spec.Tools = agentv1alpha1.ToolSet{}
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(pod.InitContainers[0].Name).To(Equal(credentialsContainerName))
		Expect(environmentOf(containerOf(pod, agentContainerName))).NotTo(HaveKey(agentTypeSherlock.configHomeVariable))
	})

	It("builds a Pod carrying a declared tool set that a namespace enforcing PodSecurity restricted admits", func() {
		name := "restricted-admits-the-config"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := podOf(statefulSetFor(name), restrictedNamespace("admits-the-config"))
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	})

	It("delivers a model's settings in the config file, its key from the Secret, and an ego by --ego-file", func() {
		name := "declares-model-and-ego"
		createSecret(credentialsSecretName(name))
		createSecret(modelKeySecretName(name))
		agent := newAgent(name)
		agent.Spec.Model = newModel(name)
		agent.Spec.Ego = testEgo
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		config := initContainerOf(pod, configContainerName)
		agentContainer := containerOf(pod, agentContainerName)

		By("writing the model's settings into the config file, naming the variable the key is in")
		written := sherlockConfig{}
		Expect(yaml.Unmarshal([]byte(environmentOf(config)[configContentVariable]), &written)).To(Succeed())
		Expect(written.Model).To(Equal(&sherlockConfigModel{
			Provider:  agent.Spec.Model.Provider,
			BaseURL:   agent.Spec.Model.BaseURL,
			Model:     agent.Spec.Model.Name,
			APIKeyEnv: agentTypeSherlock.modelKeyVariable,
		}))
		Expect(written.Embedding).To(Equal(&sherlockConfigEmbedding{
			BaseURL:   testEmbeddingBaseURL,
			Model:     testEmbeddingModel,
			APIKeyEnv: agentTypeSherlock.embeddingKeyVariable,
		}))
		Expect(written.Tools).To(BeNil())

		By("giving the agent's container, and no other, the key from the Secret the spec names")
		Expect(agentContainer.Env).To(ContainElement(corev1.EnvVar{
			Name: agentTypeSherlock.modelKeyVariable,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: modelKeySecretName(name)},
				Key:                  agent.Spec.Model.APIKeySecretRef.Key,
			}},
		}))
		Expect(environmentOf(config)).NotTo(HaveKey(agentTypeSherlock.modelKeyVariable))
		Expect(agentContainer.Env).To(ContainElement(corev1.EnvVar{
			Name: agentTypeSherlock.embeddingKeyVariable,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: modelKeySecretName(name)},
				Key:                  agent.Spec.Model.Embedding.APIKeySecretRef.Key,
			}},
		}))
		Expect(environmentOf(config)).NotTo(HaveKey(agentTypeSherlock.embeddingKeyVariable))

		By("writing the ego into a file beside the config file, and pointing the agent at it")
		egoFile := agentTypeSherlock.egoFileIn(agentTypeSherlock.configMountPath)
		Expect(environmentOf(config)).To(HaveKeyWithValue(egoContentVariable, testEgo))
		Expect(strings.Join(config.Command, " ")).To(ContainSubstring(egoFile))
		Expect(agentContainer.Args).To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, name, sherlockEgoFileFlag, egoFile}))
		Expect(environmentOf(agentContainer)).NotTo(HaveKey(egoContentVariable))
		Expect(agentContainer.Command).To(BeEmpty())
	})

	It("writes an ego file only its owner can read, holding the ego exactly as declared", func() {
		dir := GinkgoT().TempDir()
		// An ego is free text a person writes. This one closes the quoting a
		// command would carry it in.
		ego := "We answer briefly.\n\" ; touch escaped ; echo \"\n"
		file, err := agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Model: newModel("writes-an-ego")})
		Expect(err).NotTo(HaveOccurred())

		command := writeConfigCommand(dir, agentTypeSherlock, true, false, false)
		run := exec.Command(command[0], command[1:]...)
		run.Dir = dir
		run.Env = append(os.Environ(), configContentVariable+"="+file, egoContentVariable+"="+ego)
		output, err := run.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))

		for _, written := range []string{agentTypeSherlock.configFileIn(dir), agentTypeSherlock.egoFileIn(dir)} {
			info, err := os.Stat(written)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)), written)
		}

		content, err := os.ReadFile(agentTypeSherlock.egoFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal(ego))
		Expect(path.Join(dir, "escaped")).NotTo(BeAnExistingFile())
	})

	It("renders a model as sherlock's model section, and leaves out a pin section nobody declared", func() {
		file, err := agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Model: newModel("renders-a-model")})
		Expect(err).NotTo(HaveOccurred())

		Expect(file).To(Equal("embedding:\n" +
			"  api-key-env: " + agentTypeSherlock.embeddingKeyVariable + "\n" +
			"  base-url: " + testEmbeddingBaseURL + "\n" +
			"  model: " + testEmbeddingModel + "\n" +
			"model:\n" +
			"  api-key-env: " + agentTypeSherlock.modelKeyVariable + "\n" +
			"  base-url: https://api.minimax.io/v1\n" +
			"  model: MiniMax-M2\n" +
			"  provider: openai-compatible\n"))
	})

	It("renders an embeddings endpoint naming no key with no key variable, and the mock with no embedding section", func() {
		keyless := newModel("renders-a-keyless-embedding")
		keyless.Embedding.APIKeySecretRef = nil
		file, err := agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Model: keyless})
		Expect(err).NotTo(HaveOccurred())
		written := sherlockConfig{}
		Expect(yaml.Unmarshal([]byte(file), &written)).To(Succeed())
		Expect(written.Embedding).To(Equal(&sherlockConfigEmbedding{
			BaseURL: testEmbeddingBaseURL, Model: testEmbeddingModel}))
		Expect(file).NotTo(ContainSubstring(agentTypeSherlock.embeddingKeyVariable))

		mock := newModel("renders-the-mock")
		mock.Provider, mock.Embedding = testMockProvider, nil
		file, err = agentTypeSherlock.renderConfig(agentv1alpha1.AgentSpec{Model: mock})
		Expect(err).NotTo(HaveOccurred())
		Expect(file).To(HavePrefix("model:\n"))
		Expect(file).NotTo(ContainSubstring("embedding"))
	})

	It("takes the model's key and the ego back out when an Agent stops declaring them", func() {
		name := "stops-declaring-model"
		createSecret(credentialsSecretName(name))
		createSecret(modelKeySecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		declaredNothing := statefulSetFor(name).Spec.Template.Spec

		// A mock model, because a model carrying an embedding cannot be removed
		// once set (ADR 0052), and the mock carries none.
		edited := readAgent(name)
		edited.Spec.Model = newModel(name)
		edited.Spec.Model.Provider, edited.Spec.Model.Embedding = testMockProvider, nil
		edited.Spec.Ego = testEgo
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		// The control: the declaration moved the Pod, so returning to the first
		// one below is the removal and not two reads of one state.
		Expect(statefulSetFor(name).Spec.Template.Spec).NotTo(Equal(declaredNothing))

		edited = readAgent(name)
		edited.Spec.Model = nil
		edited.Spec.Ego = ""
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		Expect(statefulSetFor(name).Spec.Template.Spec).To(Equal(declaredNothing))
	})

	It("builds a Pod carrying a model and an ego that a namespace enforcing PodSecurity restricted admits", func() {
		name := "restricted-admits-the-model"
		createSecret(credentialsSecretName(name))
		createSecret(modelKeySecretName(name))
		agent := newAgent(name)
		agent.Spec.Model = newModel(name)
		agent.Spec.Ego = testEgo
		createAgent(agent)

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := podOf(statefulSetFor(name), restrictedNamespace("admits-the-model"))
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	})

	It("starts the agent under the GRN its spec carries, and an Agent a user wrote under its own name", func() {
		constructed := "starts-under-its-grn"
		createSecret(credentialsSecretName(constructed))
		agent := newAgent(constructed)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, AssignmentEpoch: "7"}
		createAgent(agent)

		// The control: an Agent carrying no identity, reconciled the same way.
		written := "starts-under-its-name"
		createSecret(credentialsSecretName(written))
		createAgent(newAgent(written))

		for _, name := range []string{constructed, written} {
			_, err := reconcileAgent(name)
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(containerOf(statefulSetFor(constructed).Spec.Template.Spec, agentContainerName).Args).
			To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, testGRN}))
		Expect(containerOf(statefulSetFor(written).Spec.Template.Spec, agentContainerName).Args).
			To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, written}))
	})

	It("passes the assignment epoch only where this operator is told the agent image accepts it", func() {
		name := "told-its-epoch"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN, AssignmentEpoch: "7"}
		createAgent(agent)

		By("reconciling with the switch off, as every deployment starts")
		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(containerOf(statefulSetFor(name).Spec.Template.Spec, agentContainerName).Args).
			NotTo(ContainElement(sherlockAssignmentEpochFlag))

		By("reconciling with it on")
		_, err = reconcileAgentRenderingEpoch(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(containerOf(statefulSetFor(name).Spec.Template.Spec, agentContainerName).Args).
			To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, testGRN, sherlockAssignmentEpochFlag, "7"}))
	})

	It("keeps the workload of an Agent whose identity is filled in after it was built", func() {
		name := "fills-its-identity"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		before := statefulSetFor(name)

		filled := readAgent(name)
		filled.Spec.Identity = &agentv1alpha1.AgentIdentity{GRN: testGRN}
		Expect(k8sClient.Update(ctx, filled)).To(Succeed())
		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())
		after := statefulSetFor(name)

		By("starting the agent under the GRN, which is what moved")
		Expect(containerOf(after.Spec.Template.Spec, agentContainerName).Args).
			To(Equal([]string{sherlockAgentCommand, sherlockAgentIDFlag, testGRN}))

		By("on the same StatefulSet and the same volume claim, not a new workload")
		Expect(after.UID).To(Equal(before.UID))
		Expect(after.Spec.VolumeClaimTemplates).To(Equal(before.Spec.VolumeClaimTemplates))
	})

	It("leaves the root filesystem of every container writable, which restricted does not ask for", func() {
		name := "writes-its-own-filesystem"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers[0].SecurityContext.ReadOnlyRootFilesystem).To(BeNil())
		Expect(pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem).To(BeNil())
	})
})

// restrictedNamespace creates a namespace enforcing PodSecurity restricted at
// the version the API server is, and returns its name. It is not deleted:
// envtest runs no namespace controller, so a deleted one stays Terminating and
// refuses everything created in it afterwards.
func restrictedNamespace(name string) string {
	GinkgoHelper()

	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"pod-security.kubernetes.io/enforce":         "restricted",
			"pod-security.kubernetes.io/enforce-version": "latest",
		},
	}})).To(Succeed())

	return name
}

// podOf is the Pod a StatefulSet's template describes, in namespace. The
// StatefulSet controller is what turns a claim template into a volume and
// envtest runs none, so a volume for each claim template is supplied here;
// PodSecurity reads an emptyDir and a claim alike.
func podOf(statefulSet *appsv1.StatefulSet, namespace string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: statefulSet.Name + "-0", Namespace: namespace},
		Spec:       *statefulSet.Spec.Template.Spec.DeepCopy(),
	}
	for _, claim := range statefulSet.Spec.VolumeClaimTemplates {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name:         claim.Name,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	return pod
}

// volumeNamed returns the Pod's volume called name, and fails the spec where it
// carries none.
func volumeNamed(pod corev1.PodSpec, name string) corev1.Volume {
	GinkgoHelper()

	for _, volume := range pod.Volumes {
		if volume.Name == name {
			return volume
		}
	}

	Fail("the Pod carries no volume named " + name)

	return corev1.Volume{}
}

// containerOf returns the Pod's container called name, and fails the spec where
// it carries none.
func containerOf(pod corev1.PodSpec, name string) corev1.Container {
	GinkgoHelper()

	for _, container := range pod.Containers {
		if container.Name == name {
			return container
		}
	}

	Fail("the Pod carries no container named " + name)

	return corev1.Container{}
}

// initContainerOf returns the Pod's init container called name, and fails the
// spec where it carries none.
func initContainerOf(pod corev1.PodSpec, name string) corev1.Container {
	GinkgoHelper()

	for _, container := range pod.InitContainers {
		if container.Name == name {
			return container
		}
	}

	Fail("the Pod carries no init container named " + name)

	return corev1.Container{}
}

// environmentOf returns a container's environment as the map a spec reads one
// variable out of, so that asserting on one says nothing about the order the
// rest are written in.
func environmentOf(container corev1.Container) map[string]string {
	environment := map[string]string{}
	for _, variable := range container.Env {
		environment[variable.Name] = variable.Value
	}

	return environment
}

// withoutWorkspace returns the Pod spec with the workspace's container and the
// variable pointing the agent at it removed. What is left is what this operator
// builds where it names no workspace image, so the two being equal is what says
// the unset flag adds nothing anywhere else.
func withoutWorkspace(pod corev1.PodSpec) corev1.PodSpec {
	stripped := *pod.DeepCopy()
	stripped.Containers = slices.DeleteFunc(stripped.Containers, func(container corev1.Container) bool {
		return container.Name == workspaceContainerName
	})
	for i := range stripped.Containers {
		stripped.Containers[i].Env = slices.DeleteFunc(stripped.Containers[i].Env,
			func(variable corev1.EnvVar) bool { return variable.Name == agentTypeSherlock.workspaceAddressVariable })
		// A slice emptied is not a slice absent, and it is the absent one the
		// Pod built without a workspace carries.
		if len(stripped.Containers[i].Env) == 0 {
			stripped.Containers[i].Env = nil
		}
	}

	return stripped
}

// withoutAdapter returns the Pod spec with the adapter's sidecar, the gateway
// address it dials, and the ego file its reply instruction brings removed. What
// is left is what this operator builds where it names no adapter image for an
// Agent declaring nothing else, so the two being equal is what says the unset
// flag adds nothing anywhere else.
func withoutAdapter(pod corev1.PodSpec) corev1.PodSpec {
	stripped := withoutToolPins(pod)
	isPlacement := func(name string) bool { return name == placementVolumeName || name == placementSecretVolumeName }
	stripped.Volumes = slices.DeleteFunc(stripped.Volumes, func(volume corev1.Volume) bool { return isPlacement(volume.Name) })
	for i := range stripped.InitContainers {
		if stripped.InitContainers[i].Name != credentialsContainerName {
			continue
		}
		stripped.InitContainers[i].VolumeMounts = slices.DeleteFunc(stripped.InitContainers[i].VolumeMounts,
			func(mount corev1.VolumeMount) bool { return isPlacement(mount.Name) })
		stripped.InitContainers[i].Command = copyCredentialsCommand(agentTypeSherlock, false)
	}
	for i := range stripped.Containers {
		if at := slices.Index(stripped.Containers[i].Args, sherlockEgoFileFlag); at >= 0 {
			stripped.Containers[i].Args = slices.Delete(stripped.Containers[i].Args, at, at+2)
		}
	}
	stripped.InitContainers = slices.DeleteFunc(stripped.InitContainers, func(container corev1.Container) bool {
		return container.Name == adapterContainerName
	})
	for i := range stripped.Containers {
		stripped.Containers[i].Env = slices.DeleteFunc(stripped.Containers[i].Env,
			func(variable corev1.EnvVar) bool { return variable.Name == agentTypeSherlock.listenAddressVariable })
	}

	return stripped
}

// withoutToolPins returns the Pod spec with the config file's volume, the
// container that writes it, the agent's mount of it and the variable pointing
// the agent at it removed. What is left is what this operator builds for an
// Agent declaring no tool set, so the two being equal is what says a declaration
// nobody made adds nothing anywhere else.
func withoutToolPins(pod corev1.PodSpec) corev1.PodSpec {
	stripped := *pod.DeepCopy()
	stripped.Volumes = slices.DeleteFunc(stripped.Volumes, func(volume corev1.Volume) bool {
		return volume.Name == configVolumeName
	})
	stripped.InitContainers = slices.DeleteFunc(stripped.InitContainers, func(container corev1.Container) bool {
		return container.Name == configContainerName
	})
	for i := range stripped.Containers {
		stripped.Containers[i].VolumeMounts = slices.DeleteFunc(stripped.Containers[i].VolumeMounts,
			func(mount corev1.VolumeMount) bool { return mount.Name == configVolumeName })
		stripped.Containers[i].Env = slices.DeleteFunc(stripped.Containers[i].Env,
			func(variable corev1.EnvVar) bool { return variable.Name == agentTypeSherlock.configHomeVariable })
		// A slice emptied is not a slice absent, and it is the absent one the
		// Pod built without a declared tool set carries.
		if len(stripped.Containers[i].Env) == 0 {
			stripped.Containers[i].Env = nil
		}
	}

	return stripped
}

// retiredToolsVolume and retiredToolsDirVariable are the volume and the
// variable an operator naming a tools image put on an agent's Pod (ADR 0019).
// The agent's image carries its tools now (ADR 0027), so no Pod carries either.
const (
	retiredToolsVolume      = "tools"
	retiredToolsDirVariable = "SHERLOCK_TOOLS_DIR"
)

// toolTreeIn lists every trace of a mounted tool tree in the Pod spec: the
// volume, a mount of it in any container, and the variable pointing at it in
// any container.
func toolTreeIn(pod corev1.PodSpec) []string {
	var found []string
	for _, volume := range pod.Volumes {
		if volume.Name == retiredToolsVolume {
			found = append(found, "volume "+volume.Name)
		}
	}
	for _, container := range slices.Concat(pod.InitContainers, pod.Containers) {
		for _, mount := range container.VolumeMounts {
			if mount.Name == retiredToolsVolume {
				found = append(found, "mount on "+container.Name)
			}
		}
		if _, set := environmentOf(container)[retiredToolsDirVariable]; set {
			found = append(found, retiredToolsDirVariable+" on "+container.Name)
		}
	}

	return found
}
