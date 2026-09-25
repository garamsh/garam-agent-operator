package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

const (
	agentContainerName = "agent"

	// credentialsContainerName is the init container that copies the projected
	// credential into the volume the agent reads it from.
	credentialsContainerName = "credentials"

	// configContainerName is the init container that writes the config file an
	// agent resolves its settings from. It is built only where an Agent declares
	// something to write into one.
	configContainerName = "config"

	// workspaceContainerName is the container serving the files an agent reads
	// and writes and the commands it runs. The agent executes nothing itself
	// and reaches this process over the Pod's loopback interface
	// (sherlock@9b0e399:internal/workspace/server/serve.go:36-42).
	workspaceContainerName = "workspace"

	credentialsVolumeName       = "credentials"
	credentialsSecretVolumeName = "credentials-secret"
	stateVolumeName             = "state"
	toolsVolumeName             = "tools"
	configVolumeName            = "config"

	// credentialsMountPath holds the copy the agent reads. credentialsSecretMountPath
	// holds the projection the kubelet writes, and only the init container mounts it.
	credentialsMountPath       = "/run/sherlock/credentials"
	credentialsSecretMountPath = "/etc/sherlock/credentials"
	stateMountPath             = "/var/lib/sherlock"

	// toolsMountPath is where the tool tree is mounted and what the agent is
	// pointed at. It sits outside the state volume's path, which the agent
	// writes and this tree is not part of.
	toolsMountPath = "/opt/sherlock/tools"

	// toolsDirVariable names the environment variable sherlock reads the directory
	// it loads its tools from. The name is that project's, because this operator
	// is writing that project's setting.
	toolsDirVariable = "SHERLOCK_TOOLS_DIR"

	// configMountPath is the configuration directory the agent is given, and the
	// one the file below is found under. It is absolute and this operator's for
	// the reason toolsMountPath is: sherlock's other two roads to a config file are
	// a path relative to a working directory the agent's image declares and a
	// flag this operator does not write, so the directory it resolves against
	// would otherwise be one this operator did not choose
	// (sherlock@fc5fca4:internal/config/config.go:371-395).
	configMountPath = "/run/sherlock/config"

	// configHomeVariable is the variable os.UserConfigDir reads that directory
	// from. It is not one of sherlock's settings and reaches no viper: sherlock's own
	// resolution is prefixed SHERLOCK_ (sherlock@fc5fca4:internal/config/config.go:276-277),
	// and this one is read by the standard library on the way to the file.
	configHomeVariable = "XDG_CONFIG_HOME"

	// configContentVariable carries the file's whole text to the init container
	// that writes it. It is set on that container and on no other, so nothing the
	// agent spawns inherits a pin set — which is the custody sherlock refuses the
	// environment road to keep (sherlock@fc5fca4:internal/config/config.go:216-234).
	configContentVariable = "AGENT_CONFIG_CONTENT"

	// configFileMask leaves the file readable by its owner and nobody else.
	// sherlock refuses a config file carrying any group or other bit
	// (sherlock@fc5fca4:internal/config/owner_only.go), so an agent handed one it
	// refuses does not start. A mask rather than a mode set afterwards, so the
	// file is never briefly readable by anyone else.
	configFileMask = "077"

	// The four settings below are sherlock's, under the SHERLOCK_ prefix and the
	// dash-to-underscore mapping every one of its settings resolves through
	// (sherlock@9b0e399:internal/config/config.go:276-277). listenAddressVariable
	// and workspaceAddressVariable are the two ends of one link: the workspace's
	// own addr and the agent's workspace-addr.
	listenAddressVariable    = "SHERLOCK_ADDR"
	workspaceAddressVariable = "SHERLOCK_WORKSPACE_ADDR"
	workspaceDirVariable     = "SHERLOCK_WORKSPACE"
	execUserVariable         = "SHERLOCK_EXEC_UID"

	// workspaceAddress is where the workspace listens and where the agent dials.
	// Both images default to it (sherlock@9b0e399:internal/config/config.go:34),
	// and it is written to both containers rather than left to them: two
	// defaults agreeing is not the same as one number this operator chose, and
	// nothing here would notice either image moving its own. It is loopback,
	// which is the only bind sherlock's unauthenticated listener accepts.
	workspaceAddress = "127.0.0.1:8081"

	// workspaceDirPath is the subtree of the state volume the workspace serves,
	// and the only part of it the workspace touches. It is absolute and this
	// operator's for the reason toolsMountPath is, and for a second: sherlock's
	// default is relative, the published workspace image declares no working
	// directory, and the /data it therefore resolves against is root-owned at
	// 0755 — which the user this Pod names cannot create in, so the workspace
	// would exit at startup
	// (sherlock@9b0e399:internal/workspace/files/files.go:56).
	workspaceDirPath = stateMountPath + "/workspace"

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
func copyCredentialsCommand() []string {
	return []string{"/bin/sh", "-ec", fmt.Sprintf(
		"for f in %s/*; do install -m %s \"$f\" %s/; done",
		credentialsSecretMountPath, credentialsCopyMode, credentialsMountPath)}
}

// configDirIn and configFileIn are where sherlock looks for a config file under
// the configuration directory dir. Both segments are that project's rather than
// this operator's to choose (sherlock@fc5fca4:internal/config/config.go:187
// and :382-395); the directory they hang off is the part this operator names.
func configDirIn(dir string) string { return dir + "/sherlock" }

func configFileIn(dir string) string { return configDirIn(dir) + "/config.yaml" }

// agentConfig is the file an agent resolves its settings from, holding what this
// operator has been taught to declare and nothing else. The field names are
// sherlock's setting names, because this operator is writing that project's file.
//
// A second key family joins it as a second field here: the file is the whole of
// what an agent is configured with, so nothing about its shape is the pins'.
type agentConfig struct {
	Tools agentConfigTools `json:"tools"`
}

type agentConfigTools struct {
	Pins map[string]string `json:"pins"`
}

// renderAgentConfig is the text of the config file an Agent's declaration
// becomes.
//
// It is marshalled rather than assembled: a pin is a string this operator does
// not read and cannot constrain, and the one place a foreign string can change
// what a file means is where somebody wrote the file's syntax by hand. Map keys
// marshal in sorted order, so one declaration renders one text and an unchanged
// Agent leaves the workload unchanged.
func renderAgentConfig(spec agentv1alpha1.AgentSpec) (string, error) {
	file, err := yaml.Marshal(agentConfig{Tools: agentConfigTools{Pins: spec.Tools.Pins}})
	if err != nil {
		return "", fmt.Errorf("render the config file of the agent: %w", err)
	}

	return string(file), nil
}

// writeConfigCommand writes the agent's config file into dir before the agent
// starts, at a mode only the user that reads it can reach.
//
// The text travels in the environment and is never part of the command. A pin
// reaches this operator from a console field it does not read, and a value
// interpolated into a command is one that can stop being a value — which is the
// ground ci.md §Security baseline states for a pipeline's inputs, met here at an
// init container's.
func writeConfigCommand(dir string) []string {
	return []string{"/bin/sh", "-ec", fmt.Sprintf(
		"umask %s && mkdir -p %s && printf '%%s' \"$%s\" > %s",
		configFileMask, configDirIn(dir), configContentVariable, configFileIn(dir))}
}

// reconcileStatefulSet brings the StatefulSet an Agent describes into being, or
// brings an existing one back to what the Agent's spec says, and returns it as
// the cluster now holds it.
func (r *AgentReconciler) reconcileStatefulSet(ctx context.Context, agent *agentv1alpha1.Agent) (*appsv1.StatefulSet, error) {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace},
	}

	operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, statefulSet, func() error {
		return r.applyAgent(agent, statefulSet)
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
// the credential's init container runs, the image an agent's tools are mounted
// from, and the image its workspace runs — is read off the reconciler, which is
// where this operator's own configuration reaches the workload.
func (r *AgentReconciler) applyAgent(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet) error {
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
	credentials.Command = copyCredentialsCommand()
	credentials.SecurityContext = containerSecurityContext()
	credentials.VolumeMounts = []corev1.VolumeMount{
		{Name: credentialsSecretVolumeName, MountPath: credentialsSecretMountPath, ReadOnly: true},
		{Name: credentialsVolumeName, MountPath: credentialsMountPath},
	}

	container := containerNamed(&statefulSet.Spec.Template.Spec.Containers, agentContainerName)
	container.Image = agent.Spec.Image
	// sherlock requires a consumer of its images to pull always, because the
	// repositories holding its bring-up builds are emptied when a release path
	// publishes: the default IfNotPresent turns a reference that stopped
	// resolving into a per-node stale cache
	// (sherlock@04ed05a:docs/architecture/adr/0020-immutable-image-repositories.md).
	container.ImagePullPolicy = corev1.PullAlways
	container.Resources = agent.Spec.Resources
	container.SecurityContext = containerSecurityContext()
	// The copy is not mounted read-only: the rule it satisfies has the reader
	// owning the file, and garam's contract expects whatever refreshes a copy to
	// do so in the Pod that reads it.
	container.VolumeMounts = []corev1.VolumeMount{
		{Name: credentialsVolumeName, MountPath: credentialsMountPath},
		{Name: stateVolumeName, MountPath: stateMountPath},
	}
	// Written on every pass, so that an operator that stops naming an image
	// stops pointing the agent at what the Pod no longer carries.
	container.Env = nil

	// The whole of the tool tree, so that an operator naming no image builds the
	// workload it built before one could be named.
	if r.ToolsImage != "" {
		statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: toolsVolumeName,
			VolumeSource: corev1.VolumeSource{
				// The kubelet mounts the image itself, so nothing copies the tree
				// and the image carrying it needs no shell and nothing writable.
				// Pulled at every start on the ground the init container's image
				// is: the reference is the deployer's and its tag need name no one
				// build, so a node's cache would leave two agents running different
				// tools under one name.
				Image: &corev1.ImageVolumeSource{Reference: r.ToolsImage, PullPolicy: corev1.PullAlways},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts,
			corev1.VolumeMount{Name: toolsVolumeName, MountPath: toolsMountPath, ReadOnly: true})
		// The variable is what points the agent at the tree; the image's own
		// entrypoint is left to run what it runs.
		container.Env = append(container.Env, corev1.EnvVar{Name: toolsDirVariable, Value: toolsMountPath})
	}

	// The agent's end of the link, written only where the other end is built:
	// an agent told where to dial with nothing listening there is the failure
	// this container exists to remove, reported one call later instead of at
	// startup.
	if r.WorkspaceImage != "" {
		container.Env = append(container.Env,
			corev1.EnvVar{Name: workspaceAddressVariable, Value: workspaceAddress})
	}

	if err := r.applyToolPins(agent, statefulSet, container); err != nil {
		return err
	}

	// Last, because appending to the container slice can move it and leave
	// every pointer taken out of it above stale.
	r.applyWorkspace(statefulSet)

	return controllerutil.SetControllerReference(agent, statefulSet, r.Scheme)
}

// applyToolPins builds the config file an Agent's declared tool set becomes and
// the init container that writes it, and takes both back out again where the
// Agent declares nothing — so that an Agent that stops declaring a tool set
// builds the workload it built before it declared one.
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
func (r *AgentReconciler) applyToolPins(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet,
	container *corev1.Container) error {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	if len(agent.Spec.Tools.Pins) == 0 {
		*initContainers = slices.DeleteFunc(*initContainers, func(initContainer corev1.Container) bool {
			return initContainer.Name == configContainerName
		})

		return nil
	}

	file, err := renderAgentConfig(agent.Spec)
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
	config.Command = writeConfigCommand(configMountPath)
	config.Env = []corev1.EnvVar{{Name: configContentVariable, Value: file}}
	config.SecurityContext = containerSecurityContext()
	config.VolumeMounts = []corev1.VolumeMount{{Name: configVolumeName, MountPath: configMountPath}}

	// Read-only, because the agent reads this file and writes nothing back to it.
	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{Name: configVolumeName, MountPath: configMountPath, ReadOnly: true})
	container.Env = append(container.Env,
		corev1.EnvVar{Name: configHomeVariable, Value: configMountPath})

	return nil
}

// applyWorkspace builds the container an agent's files and commands are served
// by, and removes it again where this operator names no workspace image — so
// that an operator that stops naming one builds the workload it built before
// one could be named. It is not folded into the tool tree's shape above: that
// one adds a volume and a variable to a container, and this one adds a
// container, which is the thing every pointer into the slice depends on.
func (r *AgentReconciler) applyWorkspace(statefulSet *appsv1.StatefulSet) {
	containers := &statefulSet.Spec.Template.Spec.Containers
	if r.WorkspaceImage == "" {
		*containers = slices.DeleteFunc(*containers, func(container corev1.Container) bool {
			return container.Name == workspaceContainerName
		})

		return
	}

	workspace := containerNamed(containers, workspaceContainerName)
	workspace.Image = r.WorkspaceImage
	// Pulled at every start on the ground the agent's image is: it is sherlock's
	// image and that project requires a consumer to pull always.
	workspace.ImagePullPolicy = corev1.PullAlways
	workspace.SecurityContext = containerSecurityContext()
	// The image's own entrypoint already serves the workspace, so every setting
	// reaches it through the environment and this operator writes no command —
	// the road the tool tree's directory already travels.
	workspace.Env = []corev1.EnvVar{
		{Name: listenAddressVariable, Value: workspaceAddress},
		{Name: workspaceDirVariable, Value: workspaceDirPath},
		// sherlock runs an exec child under the workspace's own account only
		// where this number is the uid that account already has, and refuses
		// every isolated exec otherwise
		// (sherlock@9b0e399:internal/workspace/shell/process_linux.go:131). The
		// Pod names that uid, so this operator is the only party that can tell
		// the workspace what it is.
		{Name: execUserVariable, Value: strconv.Itoa(agentRunAsUser)},
	}
	// The agent's container holds this volume too, so the two processes are
	// kept to disjoint subtrees of it: workspaceDirPath is the workspace's, and
	// nothing but these constants keeps them apart. The credential's copy is not
	// mounted here — the workspace reads no credential, and every container
	// mounting it is one more that can.
	workspace.VolumeMounts = []corev1.VolumeMount{{Name: stateVolumeName, MountPath: stateMountPath}}
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
