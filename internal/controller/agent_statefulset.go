package controller

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

const (
	agentContainerName = "agent"

	// credentialsContainerName is the init container that copies the projected
	// credential into the volume the agent reads it from.
	credentialsContainerName = "credentials"

	// configContainerName is the init container that writes the config file an
	// agent resolves its settings from, and its ego file. It is built only where
	// an Agent declares something to write into one.
	configContainerName = "config"

	// workspaceContainerName is the container serving the files an agent reads
	// and writes and the commands it runs. The agent executes nothing itself
	// and reaches this process over the Pod's loopback interface, at the address
	// the agent type's descriptor names.
	workspaceContainerName = "workspace"

	// adapterContainerName is garam's adapter: the process carrying messages
	// between garam and the agent's gateway. It is a native sidecar, an init
	// container that keeps running, because it has to outlive the agent while
	// the Pod shuts down (garam@fdfb76d:docs/architecture/adapter.md:28-33).
	adapterContainerName = "adapter"

	// adapterCredentialsMountPath is where the adapter reads the agent's
	// credential. It is this operator's path for garam's process, the same
	// for every agent type, so it is not the descriptor's.
	adapterCredentialsMountPath = "/run/garam/credentials"

	credentialsVolumeName       = "credentials"
	credentialsSecretVolumeName = "credentials-secret"
	stateVolumeName             = "state"
	configVolumeName            = "config"

	// configContentVariable carries the file's whole text to the init container
	// that writes it. It is this operator's name, set on that container and on no
	// other, so nothing the agent spawns inherits a pin set — which is the custody
	// the sherlock descriptor's agent refuses the environment road to keep
	// (sherlock@fc5fca4:internal/config/config.go:216-234).
	configContentVariable = "AGENT_CONFIG_CONTENT"

	// egoContentVariable carries the ego file's text to the same init
	// container, on the same ground.
	egoContentVariable = "AGENT_EGO_CONTENT"

	// configFileMask leaves the file readable by its owner and nobody else. The
	// sherlock descriptor's agent refuses a config file carrying any group or
	// other bit (sherlock@8218189:docs/architecture/deployment.md:78), so an
	// agent handed one it refuses does not start. A mask rather than a mode set
	// afterwards, so the file is never briefly readable by anyone else.
	configFileMask = "077"

	// workspaceDirName is the subtree of the state volume the workspace serves,
	// and the only part of it the workspace touches. The path is absolute and
	// this operator's: the sherlock descriptor's workspace defaults to a relative
	// one, its published image declares no working directory, and the /data it
	// therefore resolves against is root-owned at 0755 — which the user this Pod
	// names cannot create in, so the workspace would exit at startup
	// (sherlock@9b0e399:internal/workspace/files/files.go:56).
	workspaceDirName = "workspace"

	// credentialsFileMode keeps the projected credential files readable by the
	// group the Pod carries and by nothing else. A Secret volume's files are
	// owned by root, so owner-only would leave them unreadable to the init
	// container that copies them, which does not run as root. Tightening it back
	// to owner-only would not survive the kubelet anyway: where a Pod carries a
	// group, it ORs group-read into every file it writes into the volume.
	credentialsFileMode = 0o440

	// credentialsCopyMode is the mode the copy carries. garam's reader refuses a
	// key file any group or other bit is set on.
	credentialsCopyMode = "0600"

	// agentFSGroup is the group the kubelet gives every volume in the Pod and
	// adds to the supplementary groups of each container's initial process. It is
	// what lets the init container open the projection, whose files the kubelet
	// writes root-owned at 0440. It buys no write: the destination emptyDir
	// arrives world-writable, so a measurement finding the copy writes without
	// the group has not shown the group unearned.
	agentFSGroup = 65532

	// agentRunAsUser is the user every container of the Pod runs as, so that the
	// copy the init container makes is owned by the process that reads it. The
	// operator names it rather than reading it off the image: an owner has to be
	// one value for the whole Pod, and no image can be asked what the others run
	// as.
	agentRunAsUser = 65532
)

// containerSecurityContext is what every container of the agent's Pod carries.
// It holds the two fields PodSecurity restricted asks of a container and nothing
// else: readOnlyRootFilesystem is no part of that standard, and naming a user
// here would take the Pod's own away from whichever container named it.
func containerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// copyCredentialsCommand copies each projected credential file into the volume
// the agent reads, at a mode only its owner can reach. The glob skips the
// kubelet's dot-prefixed bookkeeping entries and names no key, so a Secret whose
// keys change does not change the workload.
func copyCredentialsCommand(descriptor agentTypeDescriptor) []string {
	return []string{"/bin/sh", "-ec", fmt.Sprintf(
		"for f in %s/*; do install -m %s \"$f\" %s/; done",
		descriptor.credentialsSecretMountPath, credentialsCopyMode, descriptor.credentialsMountPath)}
}

// writeConfigCommand writes the agent's config file into dir, and its ego file
// where ego is set, at the paths the descriptor names under it, before the agent
// starts and at a mode only the user that reads them can reach.
//
// The text travels in the environment and is never part of the command. A pin
// reaches this operator from a console field it does not read, and a value
// interpolated into a command is one that can stop being a value — which is the
// ground ci.md §Security baseline states for a pipeline's inputs, met here at an
// init container's.
func writeConfigCommand(dir string, descriptor agentTypeDescriptor, ego bool) []string {
	script := fmt.Sprintf("umask %s && %s", configFileMask,
		writeFileCommand(descriptor.configFileIn(dir), configContentVariable))
	if ego {
		script += " && " + writeFileCommand(descriptor.egoFileIn(dir), egoContentVariable)
	}

	return []string{"/bin/sh", "-ec", script}
}

// writeFileCommand is the shell that writes the text variable holds to file.
func writeFileCommand(file, variable string) string {
	return fmt.Sprintf("mkdir -p %s && printf '%%s' \"$%s\" > %s", path.Dir(file), variable, file)
}

// reconcileStatefulSet brings the StatefulSet an Agent describes into being, or
// brings an existing one back to what the Agent's spec says, and returns it as
// the cluster now holds it.
func (r *AgentReconciler) reconcileStatefulSet(ctx context.Context, agent *agentv1alpha1.Agent,
	descriptor agentTypeDescriptor) (*appsv1.StatefulSet, error) {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace},
	}

	operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, statefulSet, func() error {
		return r.applyAgent(agent, statefulSet, descriptor)
	})
	if err != nil {
		return nil, fmt.Errorf("create or update statefulset: %w", err)
	}

	if operation != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("Reconciled the StatefulSet", "statefulSet", statefulSet.Name, "operation", operation)
	}

	return statefulSet, nil
}

// claimedStorageSize is the size of the volume the StatefulSet claims for the
// agent's state, and the zero quantity when it claims none. A claim template
// cannot be changed after creation, so this is what an Agent's storage size is
// worth comparing against.
func claimedStorageSize(statefulSet *appsv1.StatefulSet) resource.Quantity {
	for _, claim := range statefulSet.Spec.VolumeClaimTemplates {
		if claim.Name == stateVolumeName {
			return *claim.Spec.Resources.Requests.Storage()
		}
	}

	return resource.Quantity{}
}

// applyAgent writes the fields an Agent's spec decides onto statefulSet and
// leaves every other field as it found it, so that an unchanged Agent produces
// an unchanged object. The fields a StatefulSet refuses a change to are written
// at creation only. What the workload carries that no Agent names — the image
// the credential's init container runs and the image its workspace runs — is
// read off the reconciler, which is where this operator's own configuration
// reaches the workload. Every path and variable name that belongs to the agent
// binary is read off descriptor.
func (r *AgentReconciler) applyAgent(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet,
	descriptor agentTypeDescriptor) error {
	if statefulSet.CreationTimestamp.IsZero() {
		labels := workloadLabels(agent)
		statefulSet.Labels = labels
		statefulSet.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		statefulSet.Spec.Template.Labels = labels
		statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{stateClaim(agent)}
	}

	// The agent's state is a single-writer store, so a second replica is never
	// correct.
	statefulSet.Spec.Replicas = ptr.To[int32](1)

	if statefulSet.Spec.Template.Spec.SecurityContext == nil {
		statefulSet.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	podSecurity := statefulSet.Spec.Template.Spec.SecurityContext
	podSecurity.FSGroup = ptr.To[int64](agentFSGroup)
	podSecurity.RunAsUser = ptr.To[int64](agentRunAsUser)
	// Naming a user PodSecurity restricted would accept is not the same as
	// asserting one: it reads runAsNonRoot and refuses a Pod that leaves it
	// unset. Both fields sit here rather than on the containers because both are
	// one value for the whole Pod.
	podSecurity.RunAsNonRoot = ptr.To(true)
	podSecurity.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	statefulSet.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name: credentialsSecretVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  agent.Spec.CredentialsSecretName,
				DefaultMode: ptr.To[int32](credentialsFileMode),
			},
		},
	}, {
		Name: credentialsVolumeName,
		VolumeSource: corev1.VolumeSource{
			// Memory-backed, so the copy never reaches the node's disk, and
			// bounded, because a memory volume that names no limit is bounded
			// only by the node.
			EmptyDir: &corev1.EmptyDirVolumeSource{
				Medium:    corev1.StorageMediumMemory,
				SizeLimit: resource.NewQuantity(1<<20, resource.BinarySI),
			},
		},
	}}

	credentials := containerNamed(&statefulSet.Spec.Template.Spec.InitContainers, credentialsContainerName)
	credentials.Image = r.CopyImage
	// Always on this operator's own ground: the copy image is the deployer's and
	// nothing here requires its tag to name one build, so a node's cache would
	// leave two agents copying a credential with different tools under one name.
	credentials.ImagePullPolicy = corev1.PullAlways
	credentials.Command = copyCredentialsCommand(descriptor)
	credentials.SecurityContext = containerSecurityContext()
	credentials.VolumeMounts = []corev1.VolumeMount{
		{Name: credentialsSecretVolumeName, MountPath: descriptor.credentialsSecretMountPath, ReadOnly: true},
		{Name: credentialsVolumeName, MountPath: descriptor.credentialsMountPath},
	}

	container := containerNamed(&statefulSet.Spec.Template.Spec.Containers, agentContainerName)
	container.Image = agent.Spec.Image
	// Always, for every type: the sherlock descriptor's agent requires a consumer
	// of its images to pull always, because an image a reference names can be
	// deleted from the repository holding it and the default IfNotPresent turns a
	// reference that stopped resolving into a per-node stale cache
	// (sherlock@8218189:docs/architecture/deployment.md:252).
	container.ImagePullPolicy = corev1.PullAlways
	container.Resources = agent.Spec.Resources
	container.SecurityContext = containerSecurityContext()
	// The copy is not mounted read-only: the rule it satisfies has the reader
	// owning the file, and garam's contract expects whatever refreshes a copy to
	// do so in the Pod that reads it.
	container.VolumeMounts = []corev1.VolumeMount{
		{Name: credentialsVolumeName, MountPath: descriptor.credentialsMountPath},
		{Name: stateVolumeName, MountPath: descriptor.stateMountPath},
	}
	// Written on every pass, so that an operator that stops naming an image
	// stops pointing the agent at what the Pod no longer carries.
	container.Env = []corev1.EnvVar{{Name: descriptor.memoryPathVariable, Value: descriptor.memoryPath()}}
	container.Args = descriptor.renderArgs(r.agentArgumentsFor(agent, descriptor))

	// The key reaches the agent's container and no other, from the Secret the
	// spec names. sherlock reads a key from a variable only
	// (sherlock@07aa5c4:internal/config/config.go:133), so the file road the
	// credential takes does not reach it.
	if agent.Spec.Model != nil {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: descriptor.modelKeyVariable,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: agent.Spec.Model.APIKeySecretRef.Name},
				Key:                  agent.Spec.Model.APIKeySecretRef.Key,
			}},
		})
	}

	// The agent's end of the link, written only where the other end is built:
	// an agent told where to dial with nothing listening there is the failure
	// this container exists to remove, reported one call later instead of at
	// startup.
	if r.WorkspaceImage != "" {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: descriptor.workspaceAddressVariable, Value: descriptor.workspaceAddress})
	}

	// The agent's end of the adapter's link, written only where the adapter is
	// built, as the workspace's address is.
	if r.adapterBuilt(agent) {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: descriptor.listenAddressVariable, Value: descriptor.gatewayAddress})
	}

	if err := r.applyConfig(agent, statefulSet, container, descriptor); err != nil {
		return err
	}
	// After the config container, so that the init containers that run to
	// completion come first; nothing the adapter reads depends on the order.
	r.applyAdapter(agent, statefulSet, descriptor)

	// Last, because appending to the container slice can move it and leave
	// every pointer taken out of it above stale.
	r.applyWorkspace(statefulSet, descriptor)

	return controllerutil.SetControllerReference(agent, statefulSet, r.Scheme)
}

// agentArgumentsFor is what an Agent's spec passes its agent on the command
// line. It reads the spec alone: the identity an agent is started under is a
// Pod input, so it is never taken from the Agent's status.
func (r *AgentReconciler) agentArgumentsFor(agent *agentv1alpha1.Agent,
	descriptor agentTypeDescriptor) agentArguments {
	// An Agent a user wrote carries no identity and is started under its own
	// name: a development identity, unique in its namespace, and not a GRN.
	args := agentArguments{agentID: agent.Name}
	if identity := agent.Spec.Identity; identity != nil {
		args.agentID = identity.GRN
		// Only where the deployment's agent image accepts the flag: one that
		// does not refuses to start on it.
		if r.RenderAssignmentEpoch {
			args.assignmentEpoch = identity.AssignmentEpoch
		}
	}
	if agent.Spec.Ego != "" {
		args.egoFile = descriptor.egoFileIn(descriptor.configMountPath)
	}

	return args
}

// applyConfig builds the config file an Agent's declared tool set and model
// become, the ego file its ego becomes, and the init container that writes
// them, and takes all of it back out again where the Agent declares nothing —
// so that an Agent that stops declaring builds the workload it built before it
// declared anything.
//
// The file is written into the Pod rather than mounted from an object of its
// own. A ConfigMap would not remove this container: the kubelet ORs group access
// into every file it writes into a volume of a Pod carrying a group, and this
// Pod carries one, so a mounted file cannot be owner-only however its mode is
// set — which is the same measurement credentialsFileMode records. It would buy
// a name to invent and a permission to hold for a value that is not secret,
// while the file still had to be copied to be readable at all.
//
// It takes no memory-backed volume, which is where this parts from the
// credential ADR 0010 delivers: that volume answers a rule about key material,
// and a pin set is public. The mode is not.
func (r *AgentReconciler) applyConfig(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet,
	container *corev1.Container, descriptor agentTypeDescriptor) error {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	ego := agent.Spec.Ego != ""
	if len(agent.Spec.Tools.Pins) == 0 && agent.Spec.Model == nil && !ego {
		*initContainers = slices.DeleteFunc(*initContainers, func(initContainer corev1.Container) bool {
			return initContainer.Name == configContainerName
		})

		return nil
	}

	file, err := descriptor.renderConfig(agent.Spec)
	if err != nil {
		return err
	}

	statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: configVolumeName,
		// Bounded by the Pod spec that carries the text rather than by a limit
		// here: one file is written and the API server is what bounds its size.
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

	config := containerNamed(initContainers, configContainerName)
	config.Image = r.CopyImage
	// Always, on the ground the credential's init container already carries: the
	// image is the deployer's and nothing here requires its tag to name one build.
	config.ImagePullPolicy = corev1.PullAlways
	config.Command = writeConfigCommand(descriptor.configMountPath, descriptor, ego)
	config.Env = []corev1.EnvVar{{Name: configContentVariable, Value: file}}
	if ego {
		config.Env = append(config.Env, corev1.EnvVar{Name: egoContentVariable, Value: agent.Spec.Ego})
	}
	config.SecurityContext = containerSecurityContext()
	config.VolumeMounts = []corev1.VolumeMount{{Name: configVolumeName, MountPath: descriptor.configMountPath}}

	// Read-only, because the agent reads this file and writes nothing back to it.
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: configVolumeName, MountPath: descriptor.configMountPath, ReadOnly: true})
	container.Env = append(container.Env,
		corev1.EnvVar{Name: descriptor.configHomeVariable, Value: descriptor.configMountPath})

	return nil
}

// applyWorkspace builds the container an agent's files and commands are served
// by, and removes it again where this operator names no workspace image — so
// that an operator that stops naming one builds the workload it built before
// one could be named. It is a function of its own because it adds a
// container, which is the thing every pointer into the slice depends on.
func (r *AgentReconciler) applyWorkspace(statefulSet *appsv1.StatefulSet, descriptor agentTypeDescriptor) {
	containers := &statefulSet.Spec.Template.Spec.Containers
	if r.WorkspaceImage == "" {
		*containers = slices.DeleteFunc(*containers, func(container corev1.Container) bool {
			return container.Name == workspaceContainerName
		})

		return
	}

	workspace := containerNamed(containers, workspaceContainerName)
	workspace.Image = r.WorkspaceImage
	// Pulled at every start on the ground the agent's image is.
	workspace.ImagePullPolicy = corev1.PullAlways
	workspace.SecurityContext = containerSecurityContext()
	// The image's own entrypoint already serves the workspace, so every setting
	// reaches it through the environment and this operator writes no command.
	workspace.Env = []corev1.EnvVar{
		{Name: descriptor.listenAddressVariable, Value: descriptor.workspaceAddress},
		{Name: descriptor.workspaceDirVariable, Value: descriptor.stateMountPath + "/" + workspaceDirName},
		// The Pod names this uid, so this operator is the only party that can
		// tell the workspace what it is.
		{Name: descriptor.execUserVariable, Value: strconv.Itoa(agentRunAsUser)},
	}
	// The agent's container holds this volume too, so the two processes are
	// kept to disjoint subtrees of it: workspaceDirName is the workspace's, and
	// nothing but these names keeps them apart. The credential's copy is not
	// mounted here — the workspace reads no credential, and every container
	// mounting it is one more that can.
	workspace.VolumeMounts = []corev1.VolumeMount{{Name: stateVolumeName, MountPath: descriptor.stateMountPath}}
}

// adapterBuilt reports whether an Agent's Pod carries garam's adapter. It needs
// an image to run, the listener to claim from, and the GRN it carries messages
// for, which garam refuses to start without; an Agent a user wrote carries no
// GRN, and garam does not know it.
func (r *AgentReconciler) adapterBuilt(agent *agentv1alpha1.Agent) bool {
	return r.AdapterImage != "" && r.GaramAddress != "" && agent.Spec.Identity != nil
}

// applyAdapter builds garam's adapter as a native sidecar of the agent's Pod,
// and removes it again where adapterBuilt says the Pod carries none — so that an
// operator that stops naming an adapter image builds the workload it built
// before one could be named. This operator places the adapter and configures
// it; what it does with messages is garam's.
//
// Every setting is one garam reads at garam@fdfb76d:internal/cli/cli.go:64-81,
// 172-178. The adapter is the agent to garam and dials the agent's gateway over
// loopback, so both identifiers are the agent's GRN: the gateway serves the
// agent under the ID it was started with, which is that GRN (ADR 0037).
func (r *AgentReconciler) applyAdapter(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet,
	descriptor agentTypeDescriptor) {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	if !r.adapterBuilt(agent) {
		*initContainers = slices.DeleteFunc(*initContainers, func(initContainer corev1.Container) bool {
			return initContainer.Name == adapterContainerName
		})

		return
	}

	grn := agent.Spec.Identity.GRN
	adapter := containerNamed(initContainers, adapterContainerName)
	adapter.Image = r.AdapterImage
	// Pulled at every start on the ground the agent's image is.
	adapter.ImagePullPolicy = corev1.PullAlways
	adapter.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
	// garam's image runs garam (garam@fdfb76d:deploy/local/Dockerfile:58), and
	// the adapter is its subcommand. No probe: the adapter listens on nothing
	// (garam@fdfb76d:docs/architecture/deployment-contract.md:473-477).
	adapter.Args = []string{"adapter"}
	adapter.SecurityContext = containerSecurityContext()
	adapter.Env = []corev1.EnvVar{
		{Name: "GARAM_ADAPTER_AGENT", Value: grn},
		{Name: "GARAM_ADAPTER_MACHINE_URL", Value: "https://" + r.GaramAddress},
		{Name: "GARAM_ADAPTER_GATEWAY_URL", Value: "http://" + descriptor.gatewayAddress},
		{Name: "GARAM_ADAPTER_GATEWAY_AGENT", Value: grn},
		{Name: "GARAM_ADAPTER_TLS_CERT_FILE", Value: adapterCredentialsMountPath + "/" + garam.CertificateKey},
		{Name: "GARAM_ADAPTER_TLS_KEY_FILE", Value: adapterCredentialsMountPath + "/" + garam.KeyKey},
		{Name: "GARAM_ADAPTER_SERVER_ROOT_FILE", Value: adapterCredentialsMountPath + "/" + garam.ServerRootKey},
	}
	adapter.VolumeMounts = adapterVolumeMounts()
}

// adapterVolumeMounts is what the adapter reads, and nothing the agent alone
// does: the copy of the agent's credential, read-only, because the adapter is
// the agent to garam and reads the same key file the agent renews. It owns that
// copy for the reason the agent does — every container runs as the Pod's user.
//
// Two mounts are to join here and are not built: the placement token Secret
// (#212), which the adapter alone reads, and the agent's outbox
// (/var/lib/sherlock/memory/outbox on the state volume,
// sherlock@ecf4621:internal/gateway/outbox.go:18-26), which waits for the
// adapter setting that names it (garam#1169).
func adapterVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{{Name: credentialsVolumeName, MountPath: adapterCredentialsMountPath, ReadOnly: true}}
}

// containerNamed returns the container called name out of containers, appending
// an empty one when it does not carry it yet. Writing through the returned
// pointer leaves the fields the API server defaulted on an existing container
// alone.
func containerNamed(containers *[]corev1.Container, name string) *corev1.Container {
	for i := range *containers {
		if (*containers)[i].Name == name {
			return &(*containers)[i]
		}
	}

	*containers = append(*containers, corev1.Container{Name: name})

	return &(*containers)[len(*containers)-1]
}

// stateClaim is the claim the agent's state volume is provisioned from.
func stateClaim(agent *agentv1alpha1.Agent) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: stateVolumeName},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: agent.Spec.StorageSize},
			},
			// nil, never "": an empty string asks for no class at all, where an
			// Agent naming none asks for the cluster's default.
			StorageClassName: agent.Spec.StorageClassName,
		},
	}
}

// workloadLabels select the Pods of one Agent's workload. They sit in the
// StatefulSet's selector, which cannot change once it exists.
func workloadLabels(agent *agentv1alpha1.Agent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     agentContainerName,
		"app.kubernetes.io/instance": agent.Name,
	}
}
