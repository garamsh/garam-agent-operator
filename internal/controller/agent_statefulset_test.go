package controller

import (
	"os"
	"os/exec"
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

// testPinnedTool is the tool the specs declare a pin for. It is gagent's one
// required tool, so a declaration leaving it out is an agent that cannot start
// whatever else the set names.
const testPinnedTool = "message_send"

// testToolPin is a pin the way an operator writes one. It is opaque to this
// operator, which is why no spec asserts anything about its shape.
const testToolPin = "sha256:aa"

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

		By("putting the credential nowhere in the environment")
		Expect(container.Env).To(BeEmpty())
		Expect(container.EnvFrom).To(BeEmpty())

		By("claiming the Agent's storage size for the volume it keeps state on")
		Expect(workload.Spec.VolumeClaimTemplates).To(HaveLen(1))
		claim := workload.Spec.VolumeClaimTemplates[0]
		Expect(claim.Spec.Resources.Requests.Storage().String()).To(Equal("1Gi"))
		Expect(container.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name:      claim.Name,
			MountPath: stateMountPath,
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
		edited.Spec.Image = "example.com/gagent:v0.2.0"
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgent(name)
		Expect(err).NotTo(HaveOccurred())

		workload := statefulSetFor(name)
		Expect(workload.Spec.Template.Spec.Containers[0].Image).To(Equal("example.com/gagent:v0.2.0"))
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
		Expect(script).To(ContainSubstring(credentialsSecretMountPath + "/*"))
		Expect(script).To(ContainSubstring(credentialsMountPath + "/"))
		Expect(credentials.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsSecretVolumeName, MountPath: credentialsSecretMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: credentialsMountPath},
		))

		By("letting neither container name a user of its own, which would leave the copy owned by somebody else")
		Expect(credentials.SecurityContext.RunAsUser).To(BeNil())
		Expect(pod.Containers).To(HaveLen(1))
		Expect(pod.Containers[0].SecurityContext.RunAsUser).To(BeNil())

		By("giving the agent the copy and not the projection")
		Expect(pod.Containers[0].VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: credentialsVolumeName, MountPath: credentialsMountPath},
			corev1.VolumeMount{Name: stateVolumeName, MountPath: stateMountPath},
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

	It("mounts the tool tree this operator names read-only, and points the agent at it", func() {
		name := "carries-a-tool-tree"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec

		By("carrying the image itself as a volume, so nothing copies the tree and no shell is asked of it")
		tools := volumeNamed(pod, toolsVolumeName)
		Expect(tools.Image).NotTo(BeNil())
		Expect(tools.Image.Reference).To(Equal(testToolsImage))
		Expect(tools.Image.PullPolicy).To(Equal(corev1.PullAlways))

		By("giving it to the agent read-only, and to no other container of the Pod")
		Expect(pod.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name:      toolsVolumeName,
			MountPath: toolsMountPath,
			ReadOnly:  true,
		}))
		Expect(pod.InitContainers[0].VolumeMounts).NotTo(ContainElement(HaveField("Name", toolsVolumeName)))

		By("pointing the agent at that path in its environment, leaving what its image runs alone")
		Expect(pod.Containers[0].Env).To(ConsistOf(corev1.EnvVar{Name: toolsDirVariable, Value: toolsMountPath}))
		Expect(pod.Containers[0].Command).To(BeEmpty())
		Expect(pod.Containers[0].Args).To(BeEmpty())
	})

	It("builds the Pod it built before a tool tree existed where this operator names no tools image", func() {
		shared := "shared-credentials"
		createSecret(shared)

		By("reconciling an Agent while this operator names a tools image")
		named := "tools-image-named"
		withTools := newAgent(named)
		withTools.Spec.CredentialsSecretName = shared
		createAgent(withTools)
		_, err := reconcileAgentWithTools(named)
		Expect(err).NotTo(HaveOccurred())

		By("reconciling an Agent identical to it while this operator names none")
		unnamed := "tools-image-unset"
		withoutTools := newAgent(unnamed)
		withoutTools.Spec.CredentialsSecretName = shared
		createAgent(withoutTools)
		_, err = reconcileAgent(unnamed)
		Expect(err).NotTo(HaveOccurred())

		unset := statefulSetFor(unnamed).Spec.Template.Spec

		By("carrying no volume, no mount and no environment variable for a tree it was given none of")
		Expect(unset.Volumes).NotTo(ContainElement(HaveField("Name", toolsVolumeName)))
		Expect(unset.Containers[0].VolumeMounts).NotTo(ContainElement(HaveField("Name", toolsVolumeName)))
		Expect(unset.Containers[0].Env).To(BeEmpty())

		set := statefulSetFor(named).Spec.Template.Spec

		By("differing from the Pod built with one, which is what leaves the comparison below something to isolate")
		Expect(set).NotTo(Equal(unset))

		By("differing from it in those three places and in nothing else")
		Expect(withoutToolTree(set)).To(Equal(unset))
	})

	It("builds a Pod carrying the tool tree that a namespace enforcing PodSecurity restricted admits", func() {
		name := "tools-satisfy-restricted"
		createSecret(credentialsSecretName(name))
		createAgent(newAgent(name))

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())
		namespace := restrictedNamespace("psa-" + name)

		By("creating the Pod the StatefulSet describes, which carries the image volume and which is admitted")
		admitted := podOf(statefulSetFor(name), namespace)
		Expect(volumeNamed(admitted.Spec, toolsVolumeName).Image).NotTo(BeNil())
		Expect(k8sClient.Create(ctx, admitted)).To(Succeed())

		By("creating the same Pod carrying a volume type the standard names, which it refuses")
		refused := podOf(statefulSetFor(name), namespace)
		refused.Name += "-host-path"
		refused.Spec.Volumes = append(refused.Spec.Volumes, corev1.Volume{
			Name:         "host",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}},
		})
		Expect(k8sClient.Create(ctx, refused)).
			To(MatchError(ContainSubstring(`violates PodSecurity "restricted:latest"`)))
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
		Expect(listen[listenAddressVariable]).To(Equal(workspaceAddress))
		Expect(environmentOf(pod.Containers[0])[workspaceAddressVariable]).To(Equal(listen[listenAddressVariable]))

		By("giving it a directory on the volume the agent's state is claimed on, which it can create in")
		Expect(workspace.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: stateVolumeName, MountPath: stateMountPath}))
		Expect(listen[workspaceDirVariable]).To(HavePrefix(stateMountPath + "/"))

		By("leaving what its image runs alone, and mounting it no credential it does not read")
		Expect(workspace.Command).To(BeEmpty())
		Expect(workspace.Args).To(BeEmpty())
		Expect(workspace.VolumeMounts).NotTo(ContainElement(HaveField("Name", credentialsVolumeName)))
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

		By("stating that same user as the account exec children run under, which gagent refuses to guess")
		Expect(environmentOf(workspace)[execUserVariable]).
			To(Equal(strconv.FormatInt(*pod.SecurityContext.RunAsUser, 10)))
	})

	It("builds the Pod it built before a workspace existed where this operator names no workspace image", func() {
		shared := "shared-workspace-credentials"
		createSecret(shared)

		By("reconciling an Agent while this operator names a workspace image")
		named := "workspace-image-named"
		withWorkspace := newAgent(named)
		withWorkspace.Spec.CredentialsSecretName = shared
		createAgent(withWorkspace)
		_, err := reconcileAgentWithWorkspace(named)
		Expect(err).NotTo(HaveOccurred())

		By("reconciling an Agent identical to it while this operator names none")
		unnamed := "workspace-image-unset"
		withoutWorkspaceImage := newAgent(unnamed)
		withoutWorkspaceImage.Spec.CredentialsSecretName = shared
		createAgent(withoutWorkspaceImage)
		_, err = reconcileAgent(unnamed)
		Expect(err).NotTo(HaveOccurred())

		unset := statefulSetFor(unnamed).Spec.Template.Spec

		By("carrying no second container and telling the agent to dial nothing")
		Expect(unset.Containers).To(HaveLen(1))
		Expect(unset.Containers[0].Env).To(BeEmpty())

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
		Expect(pod.Containers[0].Env).To(BeEmpty())
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

		By("creating the same Pod with the capability gagent needs only on the root path, which it refuses")
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

	It("writes the tool set an Agent declares into a file the agent reads, and points the agent at it", func() {
		name := "declares-a-tool-set"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin, testSecondTool: testSecondPin}
		createAgent(agent)

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec

		By("writing it into a volume of its own, which is not the memory a credential's copy takes")
		written := volumeNamed(pod, configVolumeName)
		Expect(written.EmptyDir).NotTo(BeNil())
		Expect(written.EmptyDir.Medium).To(BeEmpty())

		By("writing it before the agent starts, at a mode gagent does not refuse")
		config := initContainerOf(pod, configContainerName)
		Expect(config.Image).To(Equal(testCopyImage))
		Expect(config.ImagePullPolicy).To(Equal(corev1.PullAlways))
		script := strings.Join(config.Command, " ")
		Expect(script).To(ContainSubstring("umask 077"))
		Expect(script).To(ContainSubstring(configFileIn(configMountPath)))
		Expect(config.VolumeMounts).To(ConsistOf(
			corev1.VolumeMount{Name: configVolumeName, MountPath: configMountPath}))

		By("carrying the file's text to that container and to nothing the agent spawns")
		Expect(environmentOf(config)).To(HaveKeyWithValue(configContentVariable,
			SatisfyAll(ContainSubstring(testPinnedTool+": "+testToolPin), ContainSubstring(testSecondTool+": "+testSecondPin))))
		agentContainer := containerOf(pod, agentContainerName)
		Expect(environmentOf(agentContainer)).NotTo(HaveKey(configContentVariable))

		By("giving the agent the file read-only and the directory it resolves one from")
		Expect(agentContainer.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: configVolumeName, MountPath: configMountPath, ReadOnly: true}))
		Expect(environmentOf(agentContainer)).To(HaveKeyWithValue(configHomeVariable, configMountPath))
	})

	It("writes a config file only its owner can read, out of text no pin can turn into a command", func() {
		dir := GinkgoT().TempDir()
		// A pin is a string this operator does not read and a console user
		// types. This one closes the quoting a command would carry it in.
		pins := map[string]string{
			testSecondTool: `sha256:bb" ; touch escaped ; echo "`,
			testPinnedTool: testToolPin,
		}
		file, err := renderAgentConfig(agentv1alpha1.AgentSpec{Tools: agentv1alpha1.ToolSet{Pins: pins}})
		Expect(err).NotTo(HaveOccurred())

		command := writeConfigCommand(dir)
		run := exec.Command(command[0], command[1:]...)
		run.Dir = dir
		run.Env = append(os.Environ(), configContentVariable+"="+file)
		output, err := run.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))

		By("leaving a file gagent's owner-only rule accepts")
		info, err := os.Stat(configFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		By("landing every pin in it exactly as it was declared")
		content, err := os.ReadFile(configFileIn(dir))
		Expect(err).NotTo(HaveOccurred())
		written := agentConfig{}
		Expect(yaml.Unmarshal(content, &written)).To(Succeed())
		Expect(written.Tools.Pins).To(Equal(pins))
	})

	It("renders one declaration as one text, whatever order the declaration is held in", func() {
		// Three tools, held in an order that is not the sorted one. A Go map is
		// iterated in no fixed order, so a render reading it directly would put
		// the same declaration in the workload differently from pass to pass,
		// and every pass would rewrite the StatefulSet.
		pins := map[string]string{"web_fetch": "sha256:cc", testPinnedTool: testToolPin, testSecondTool: testSecondPin}

		file, err := renderAgentConfig(agentv1alpha1.AgentSpec{Tools: agentv1alpha1.ToolSet{Pins: pins}})
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

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())
		withoutPins := statefulSetFor(name).Spec.Template.Spec

		edited := readAgent(name)
		edited.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgentWithTools(name)
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

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(statefulSetFor(name).Spec.Template.Spec.InitContainers).To(HaveLen(2))

		edited := readAgent(name)
		edited.Spec.Tools = agentv1alpha1.ToolSet{}
		Expect(k8sClient.Update(ctx, edited)).To(Succeed())

		_, err = reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())

		pod := statefulSetFor(name).Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(pod.InitContainers[0].Name).To(Equal(credentialsContainerName))
		Expect(environmentOf(containerOf(pod, agentContainerName))).NotTo(HaveKey(configHomeVariable))
	})

	It("builds a Pod carrying a declared tool set that a namespace enforcing PodSecurity restricted admits", func() {
		name := "restricted-admits-the-config"
		createSecret(credentialsSecretName(name))
		agent := newAgent(name)
		agent.Spec.Tools.Pins = map[string]string{testPinnedTool: testToolPin}
		createAgent(agent)

		_, err := reconcileAgentWithTools(name)
		Expect(err).NotTo(HaveOccurred())

		pod := podOf(statefulSetFor(name), restrictedNamespace("admits-the-config"))
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
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
// envtest runs none, so the state volume is supplied here; PodSecurity reads an
// emptyDir and a claim alike.
func podOf(statefulSet *appsv1.StatefulSet, namespace string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: statefulSet.Name + "-0", Namespace: namespace},
		Spec:       *statefulSet.Spec.Template.Spec.DeepCopy(),
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         stateVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

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
			func(variable corev1.EnvVar) bool { return variable.Name == workspaceAddressVariable })
		// A slice emptied is not a slice absent, and it is the absent one the
		// Pod built without a workspace carries.
		if len(stripped.Containers[i].Env) == 0 {
			stripped.Containers[i].Env = nil
		}
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
			func(variable corev1.EnvVar) bool { return variable.Name == configHomeVariable })
		// A slice emptied is not a slice absent, and it is the absent one the
		// Pod built without a declared tool set carries.
		if len(stripped.Containers[i].Env) == 0 {
			stripped.Containers[i].Env = nil
		}
	}

	return stripped
}

// withoutToolTree returns the Pod spec with the tool tree's volume, its mount
// and the variable pointing at it removed. What is left is what this operator
// builds where it names no tools image, so the two being equal is what says the
// unset flag adds nothing anywhere else.
func withoutToolTree(pod corev1.PodSpec) corev1.PodSpec {
	stripped := *pod.DeepCopy()
	stripped.Volumes = slices.DeleteFunc(stripped.Volumes, func(volume corev1.Volume) bool {
		return volume.Name == toolsVolumeName
	})
	for i := range stripped.Containers {
		stripped.Containers[i].VolumeMounts = slices.DeleteFunc(stripped.Containers[i].VolumeMounts,
			func(mount corev1.VolumeMount) bool { return mount.Name == toolsVolumeName })
		stripped.Containers[i].Env = slices.DeleteFunc(stripped.Containers[i].Env,
			func(variable corev1.EnvVar) bool { return variable.Name == toolsDirVariable })
		// A slice emptied is not a slice absent, and it is the absent one the
		// Pod built without a tool tree carries.
		if len(stripped.Containers[i].Env) == 0 {
			stripped.Containers[i].Env = nil
		}
	}

	return stripped
}
