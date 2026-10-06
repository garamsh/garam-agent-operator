package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/agentname"
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
	adapterContainerName = agentname.AdapterContainer

	// adapterCredentialsMountPath is where the adapter reads the agent's
	// credential. It is this operator's path for garam's process, the same
	// for every agent type, so it is not the descriptor's.
	adapterCredentialsMountPath = "/run/garam/credentials"

	// placementMountPath is where the adapter reads the placement token, as the
	// file named placementTokenKey; placementSecretMountPath is where the copying
	// init container reads the Secret's projection.
	placementMountPath       = "/run/garam/placement"
	placementSecretMountPath = "/etc/garam/placement"

	credentialsVolumeName       = "credentials"
	credentialsSecretVolumeName = "credentials-secret"

	// controlRootMountPath is where the adapter reads the root the control
	// service's serving certificate chains to, as controlRootFile; outboxMountPath
	// is where it reads and clears the agent's outbox. Both are this operator's
	// paths for garam's process (#218).
	controlRootMountPath = "/run/garam/control"
	controlRootFile      = "root.pem"
	outboxMountPath      = "/run/garam/outbox"

	// controlRootVolumeName holds the control root the config writer copies in,
	// which only the adapter reads.
	controlRootVolumeName = "control-root"

	// controlRootContentVariable carries the control root's text to the config
	// writer, on configContentVariable's ground.
	controlRootContentVariable = "AGENT_CONTROL_ROOT_CONTENT"

	// outboxContainerName is the init container that makes the agent's outbox
	// directory on the state claim before the adapter mounts it, and
	// outboxStateMountPath is where it mounts that claim. Created by anything
	// else, the directory the adapter's subPath names would be the kubelet's,
	// owned by root, and the agent could not write it.
	outboxContainerName  = "outbox"
	outboxStateMountPath = "/run/garam/state"

	// outboxDirMode lets the Pod's user and group write the outbox and nobody
	// else; sherlock's own creation of it leaves an existing directory as it
	// is (sherlock@44aaa55:tools/message_send/message_send.go:108).
	outboxDirMode = "0770"

	// memoryPageBytes is the largest page a Linux node's kernel runs with (64 KiB
	// on arm64 and ppc64le, 4 KiB on amd64). A memory volume is tmpfs, which
	// charges each file whole pages, so its sizeLimit is counted in pages (#265).
	memoryPageBytes = 64 << 10

	// placementVolumeFiles is what the placement volume holds: the token, copied
	// alone by the credentials init container (ADR 0042). The token is
	// placementTokenBytes of randomness hex-encoded, 64 bytes, inside one page.
	placementVolumeFiles = 1

	// placementVolumeName holds the copy of the placement token, which only the
	// adapter mounts; placementSecretVolumeName is the Secret's projection, which
	// only the init container that copies it mounts.
	placementVolumeName       = "placement"
	placementSecretVolumeName = "placement-secret"
	configVolumeName          = "config"

	// stateVolumeName is the claim the agent's state is kept on: its memory store
	// and the outbox beside it. Only the agent's container mounts it. The name is
	// the one the claim had when the workspace shared it, so the claim an existing
	// agent already holds is the one it keeps (ADR 0044).
	stateVolumeName = "state"

	// workspaceVolumeName is the claim the workspace serves its files from. Only
	// the workspace's container mounts it, so nothing the agent runs reaches the
	// agent's state (ADR 0044).
	workspaceVolumeName = "workspace"

	// seedContainerName is the init container that copies an upgraded agent's
	// workspace off its state claim onto its own, once. It is built only on a
	// StatefulSet carrying seedAnnotation.
	seedContainerName = "workspace-seed"

	// seedAnnotation marks a StatefulSet created in place of one whose workspace
	// shared the state claim, which is the only kind whose Pod seeds the
	// workspace. Its value names the claim the seed copies from.
	seedAnnotation = "agent.garam.sh/workspace-seed"

	// seedStateMountPath and seedWorkspaceMountPath are where the seed container
	// reads the state claim and writes the workspace claim. They are this
	// operator's paths for its own script, so they are not the descriptor's.
	seedStateMountPath     = "/run/garam/seed/state"
	seedWorkspaceMountPath = "/run/garam/seed/workspace"

	// seededMarker is the file the seed leaves at the root of the workspace
	// claim once its copy is durable. It is beside the directory the workspace
	// serves, not in it.
	seededMarker = ".seeded"

	// configContentVariable carries the file's whole text to the init container
	// that writes it. It is this operator's name, set on that container and on no
	// other, so nothing the agent spawns inherits a pin set — which is the custody
	// the sherlock descriptor's agent refuses the environment road to keep
	// (sherlock@fc5fca4:internal/config/config.go:216-234).
	configContentVariable = "AGENT_CONFIG_CONTENT"

	// egoContentVariable carries the ego file's text to the same init
	// container, on the same ground.
	egoContentVariable = "AGENT_EGO_CONTENT"

	// instructionsContentVariable carries the operator instructions file's text
	// to the same init container, on the same ground.
	instructionsContentVariable = "AGENT_INSTRUCTIONS_CONTENT"

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

// The settings garam's adapter reads, by the names garam@e81a1e0 gives them
// (internal/cli/cli.go:182-191). GATEWAY_AGENT is garam@fdfb76d's, which that
// adapter requires (internal/cli/delivery.go:90-91) and garam@e81a1e0 reads
// nowhere.
const (
	adapterAgentSetting          = "GARAM_ADAPTER_AGENT"
	adapterMachineURLSetting     = "GARAM_ADAPTER_MACHINE_URL"
	adapterGatewayURLSetting     = "GARAM_ADAPTER_GATEWAY_URL"
	adapterGatewayAgentSetting   = "GARAM_ADAPTER_GATEWAY_AGENT"
	adapterCertFileSetting       = "GARAM_ADAPTER_TLS_CERT_FILE"
	adapterKeyFileSetting        = "GARAM_ADAPTER_TLS_KEY_FILE"
	adapterServerRootSetting     = "GARAM_ADAPTER_SERVER_ROOT_FILE"
	adapterControlURLSetting     = "GARAM_ADAPTER_CONTROL_URL"
	adapterControlRootSetting    = "GARAM_ADAPTER_CONTROL_ROOT_FILE"
	adapterPlacementTokenSetting = "GARAM_ADAPTER_PLACEMENT_TOKEN_FILE"
	adapterOutboxDirSetting      = "GARAM_ADAPTER_OUTBOX_DIR"
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
// the agent reads, at a mode only its owner can reach, and the placement token
// the same way into the volume the adapter reads where placement is set. The
// glob skips the kubelet's dot-prefixed bookkeeping entries and names no key,
// so a Secret whose keys change does not change the workload.
func copyCredentialsCommand(descriptor agentTypeDescriptor, placement bool) []string {
	script := copyFilesCommand(descriptor.credentialsSecretMountPath, descriptor.credentialsMountPath)
	if placement {
		script += "; " + copyFilesCommand(placementSecretMountPath, placementMountPath)
	}

	return shellCommand(script)
}

// shellCommand runs script in a shell that stops at the first command to fail.
func shellCommand(script string) []string {
	return []string{"/bin/sh", "-ec", script}
}

// copyFilesCommand is the shell that copies every file in from into to.
func copyFilesCommand(from, to string) string {
	return fmt.Sprintf("for f in %s/*; do install -m %s \"$f\" %s/; done", from, credentialsCopyMode, to)
}

// writeConfigCommand writes the agent's config file into dir, its ego file where
// ego is set, and its operator instructions file where instructions is set, at
// the paths the descriptor names under it, before the agent starts and at a mode
// only the user that reads them can reach.
//
// The text travels in the environment and is never part of the command. A pin
// reaches this operator from a console field it does not read, and a value
// interpolated into a command is one that can stop being a value — which is the
// ground ci.md §Security baseline states for a pipeline's inputs, met here at an
// init container's.
func writeConfigCommand(dir string, descriptor agentTypeDescriptor, ego, instructions, controlRoot bool) []string {
	script := fmt.Sprintf("umask %s && %s", configFileMask,
		writeFileCommand(descriptor.configFileIn(dir), configContentVariable))
	if ego {
		script += " && " + writeFileCommand(descriptor.egoFileIn(dir), egoContentVariable)
	}
	if instructions {
		script += " && " + writeFileCommand(descriptor.instructionsFileIn(dir), instructionsContentVariable)
	}
	if controlRoot {
		script += " && " + writeFileCommand(controlRootMountPath+"/"+controlRootFile, controlRootContentVariable)
	}

	return shellCommand(script)
}

// writeFileCommand is the shell that writes the text variable holds to file.
func writeFileCommand(file, variable string) string {
	return fmt.Sprintf("mkdir -p %s && printf '%%s' \"$%s\" > %s", path.Dir(file), variable, file)
}

// seedWorkspaceCommand copies the workspace directory from, on the state claim,
// into to, on the workspace claim, unless marker says it was already copied. It
// copies and never moves, so the source stays where it was. The marker is
// written only after a sync makes the copy durable, and synced itself, so a stop
// at any point leaves either a copy that is done or a marker that is absent and a
// copy that runs again. A state claim with no workspace directory is seeded with
// nothing.
func seedWorkspaceCommand(from, to, marker string) []string {
	script := fmt.Sprintf("if [ ! -e %[3]s ]; then "+
		"if [ -d %[1]s ]; then mkdir -p %[2]s && cp -a %[1]s/. %[2]s/; fi; "+
		"sync && : > %[3]s && sync; fi", from, to, marker)

	return shellCommand(script)
}

// errReplacing reports that the StatefulSet is being replaced, so there is none
// to reconcile until the old one is gone.
var errReplacing = errors.New("the statefulset is being replaced")

// replicasFor is the number of replicas the Agent's workload runs: one, or none
// while it is suspended or stopped.
func replicasFor(agent *agentv1alpha1.Agent) int32 {
	if heldStopped(agent) {
		return 0
	}

	return 1
}

// heldStopped reports whether the spec keeps the agent stopped: a person's
// suspension, or the control service's stop (ADR 0057). Both take the same
// road: no replica, and the Pod released only on the writer fence's evidence.
func heldStopped(agent *agentv1alpha1.Agent) bool {
	return agent.Spec.Suspended || agent.Spec.Stopped
}

// reconcileStatefulSet brings the StatefulSet an Agent describes into being, or
// brings an existing one back to what the Agent's spec says, and returns it as
// the cluster now holds it. A StatefulSet whose workspace shares the state claim
// is replaced first, which returns errReplacing until the old one is gone.
func (r *AgentReconciler) reconcileStatefulSet(ctx context.Context, agent *agentv1alpha1.Agent,
	descriptor agentTypeDescriptor) (*appsv1.StatefulSet, error) {
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace},
	}

	seed, err := r.replaceSharedShape(ctx, agent, statefulSet)
	if err != nil {
		return nil, err
	}

	operation, err := controllerutil.CreateOrUpdate(ctx, r.Client, statefulSet, func() error {
		if statefulSet.CreationTimestamp.IsZero() && seed {
			statefulSet.Annotations = map[string]string{seedAnnotation: stateVolumeName}
		}

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

// replaceSharedShape deletes a StatefulSet whose workspace shares the state
// claim, leaving its Pod and its claims where they are, and reports whether the
// one to be created in its place seeds the workspace. It deletes one only where
// MigrateSharedClaims is set; otherwise the StatefulSet is kept in its shape
// (ADR 0047). A claim template cannot be
// changed after creation, so a second claim needs a second StatefulSet (ADR
// 0044).
//
// The old Pod is the only writer of the state claim throughout. Orphaned, it
// keeps running alone until the new StatefulSet adopts it by its labels and
// deletes it to roll it, and its writer fence then holds it until its writers
// are seen to stop (ADR 0042). The new Pod has the old one's name, so it cannot
// be created while the old one exists, and the state claim is the same claim by
// name, so the fence reads the claim the old Pod started on.
//
// Whether to seed is read off the cluster rather than remembered, so a manager
// stopped between the delete and the create decides it again the same way: a
// StatefulSet created where the state claim exists and the workspace claim does
// not is one replacing the shared shape. A new agent has neither, and a
// StatefulSet deleted by hand from the separate shape leaves both.
func (r *AgentReconciler) replaceSharedShape(ctx context.Context, agent *agentv1alpha1.Agent,
	statefulSet *appsv1.StatefulSet) (seed bool, err error) {
	existing := &appsv1.StatefulSet{}
	err = r.Get(ctx, client.ObjectKeyFromObject(statefulSet), existing)
	if err == nil {
		// An existing StatefulSet decided its seed when it was created, and its
		// annotation carries that decision; only a missing one is decided here.
		// A shared-shape one is kept as it is unless this operator is told to
		// replace it, and applyAgent then renders it in that shape (ADR 0047).
		if hasWorkspaceClaim(existing) || !r.MigrateSharedClaims {
			return false, nil
		}
		// A shared-shape StatefulSet still deleting is deleted again, which
		// changes nothing, and is waited for the same way.

		uid := existing.UID
		err := r.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationOrphan),
			client.Preconditions{UID: &uid})
		if err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete the statefulset its workspace shares a claim in, leaving its pod: %w", err)
		}
		logf.FromContext(ctx).Info("Replacing the StatefulSet to give the workspace a claim of its own",
			"statefulSet", existing.Name)

		return false, errReplacing
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get statefulset: %w", err)
	}

	stateClaimed, err := r.claimExists(ctx, agent, stateVolumeName)
	if err != nil {
		return false, err
	}
	workspaceClaimed, err := r.claimExists(ctx, agent, workspaceVolumeName)
	if err != nil {
		return false, err
	}

	return stateClaimed && !workspaceClaimed, nil
}

// claimExists reports whether the claim the StatefulSet makes from the template
// called template exists for the Agent's Pod. It reads uncached, as the fence
// reads claims.
func (r *AgentReconciler) claimExists(ctx context.Context, agent *agentv1alpha1.Agent, template string) (bool, error) {
	name := template + "-" + agentPodName(agent)
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: name}, &corev1.PersistentVolumeClaim{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get claim %q: %w", name, err)
	}

	return true, nil
}

// claimTemplate is the StatefulSet's claim template called name, nil where it
// has none.
func claimTemplate(statefulSet *appsv1.StatefulSet, name string) *corev1.PersistentVolumeClaim {
	for i := range statefulSet.Spec.VolumeClaimTemplates {
		if statefulSet.Spec.VolumeClaimTemplates[i].Name == name {
			return &statefulSet.Spec.VolumeClaimTemplates[i]
		}
	}

	return nil
}

// hasWorkspaceClaim reports whether the StatefulSet claims the workspace
// separately. One kept in the shared shape does not, so it has no workspace size
// to compare and its workspace mounts the state claim (ADR 0047).
func hasWorkspaceClaim(statefulSet *appsv1.StatefulSet) bool {
	return claimTemplate(statefulSet, workspaceVolumeName) != nil
}

// claimedStorageSize is the size of the volume the StatefulSet claims from the
// template called name, and the zero quantity when it claims none. A claim
// template cannot be changed after creation, so this is what an Agent's storage
// size is worth comparing against.
func claimedStorageSize(statefulSet *appsv1.StatefulSet, name string) resource.Quantity {
	if claim := claimTemplate(statefulSet, name); claim != nil {
		return *claim.Spec.Resources.Requests.Storage()
	}

	return resource.Quantity{}
}

// workspaceStorageSize is the size an Agent asks for its workspace's volume:
// its own where it names one, and its state's where it does not.
func workspaceStorageSize(agent *agentv1alpha1.Agent) resource.Quantity {
	if agent.Spec.WorkspaceStorageSize != nil {
		return *agent.Spec.WorkspaceStorageSize
	}

	return agent.Spec.StorageSize
}

// claimedStorageClass is the storage class the StatefulSet claims the agent's
// state volume from, nil where it names none.
func claimedStorageClass(statefulSet *appsv1.StatefulSet) *string {
	for _, claim := range statefulSet.Spec.VolumeClaimTemplates {
		if claim.Name == stateVolumeName {
			return claim.Spec.StorageClassName
		}
	}

	return nil
}

// describeStorageClass names a storage class in a message, and says where none
// is named.
func describeStorageClass(class *string) string {
	if class == nil {
		return "the cluster's default"
	}

	return fmt.Sprintf("%q", *class)
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
		statefulSet.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			claim(stateVolumeName, agent.Spec.StorageSize, agent),
			claim(workspaceVolumeName, workspaceStorageSize(agent), agent),
		}
	}

	// The agent's state is a single-writer store, so a second replica is never
	// correct. A suspended agent has none, and its Pod is released by its writer
	// fence (ADR 0046).
	statefulSet.Spec.Replicas = ptr.To(replicasFor(agent))

	// Every Pod the StatefulSet creates carries the writer fence, so a deleted
	// one is held until its writers are seen to stop (ADR 0042).
	statefulSet.Spec.Template.Finalizers = []string{writerStoppedFinalizer}

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

	// The placement token reaches the adapter by the credential's route: the
	// same init container copies it out of its Secret into a memory volume the
	// Pod's user owns, and only the adapter mounts that copy.
	placement := r.adapterBuilt(agent)
	if placement {
		statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: placementSecretVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  placementSecretName(agent),
					DefaultMode: ptr.To[int32](credentialsFileMode),
				},
			},
		}, corev1.Volume{
			Name: placementVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(placementVolumeFiles*memoryPageBytes, resource.BinarySI),
				},
			},
		})
	}

	credentials := containerNamed(&statefulSet.Spec.Template.Spec.InitContainers, credentialsContainerName)
	credentials.Image = r.CopyImage
	// Always on this operator's own ground: the copy image is the deployer's and
	// nothing here requires its tag to name one build, so a node's cache would
	// leave two agents copying a credential with different tools under one name.
	credentials.ImagePullPolicy = corev1.PullAlways
	credentials.Command = copyCredentialsCommand(descriptor, placement)
	credentials.SecurityContext = containerSecurityContext()
	credentials.VolumeMounts = []corev1.VolumeMount{
		{Name: credentialsSecretVolumeName, MountPath: descriptor.credentialsSecretMountPath, ReadOnly: true},
		{Name: credentialsVolumeName, MountPath: descriptor.credentialsMountPath},
	}
	if placement {
		credentials.VolumeMounts = append(credentials.VolumeMounts,
			corev1.VolumeMount{Name: placementSecretVolumeName, MountPath: placementSecretMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: placementVolumeName, MountPath: placementMountPath})
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
		if key := embeddingKeyOf(agent); key != nil {
			container.Env = append(container.Env, corev1.EnvVar{
				Name: descriptor.embeddingKeyVariable,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: key.Name},
					Key:                  key.Key,
				}},
			})
		}
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

	controlRoot, err := r.controlRootFor(agent)
	if err != nil {
		return err
	}
	if err := r.applyConfig(agent, statefulSet, container, descriptor, controlRoot); err != nil {
		return err
	}
	applySeed(r.CopyImage, statefulSet)
	r.applyOutbox(agent, statefulSet, descriptor)
	// After the init containers that run to completion, which make what the
	// adapter mounts: the outbox directory and the control root.
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
	if r.egoFor(agent, descriptor) != "" {
		args.egoFile = descriptor.egoFileIn(descriptor.configMountPath)
	}
	if r.instructionsFor(agent, descriptor) != "" {
		args.instructionsFile = descriptor.instructionsFileIn(descriptor.configMountPath)
	}

	return args
}

// instructionsFor is the text of the operator instructions file an Agent's
// agent is given: garam's reply instruction wherever the adapter is placed,
// because only then does a message in garam's envelope arrive, and this operator
// renders the file at all. The agent composes it between its ego and its own
// contract and never in place of the ego (ADR 0045). Empty means no file.
func (r *AgentReconciler) instructionsFor(agent *agentv1alpha1.Agent, descriptor agentTypeDescriptor) string {
	if !r.RenderInstructionsFile || !r.adapterBuilt(agent) {
		return ""
	}

	return descriptor.garamReplyInstruction
}

// egoFor is the text of the ego file an Agent's agent is given: the ego its
// spec declares, and nothing of this operator's where the reply instruction
// goes to the instructions file. The spec holds what its author wrote; an empty
// ego means no ego file, and the agent's image keeps its default ego.
//
// Where this operator renders no instructions file, because the deployment's
// agent image predates the flag, the instruction is joined to the ego wherever
// the adapter is placed, and where the spec declares no ego it is the whole
// file and replaces the image's default ego (ADR 0041, kept behind ADR 0045's
// switch).
func (r *AgentReconciler) egoFor(agent *agentv1alpha1.Agent, descriptor agentTypeDescriptor) string {
	if r.RenderInstructionsFile || !r.adapterBuilt(agent) {
		return agent.Spec.Ego
	}
	if agent.Spec.Ego == "" {
		return descriptor.garamReplyInstruction
	}

	return strings.TrimRight(agent.Spec.Ego, "\n") + "\n\n" + descriptor.garamReplyInstruction
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
	container *corev1.Container, descriptor agentTypeDescriptor, controlRoot []byte) error {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	egoText := r.egoFor(agent, descriptor)
	ego := egoText != ""
	instructionsText := r.instructionsFor(agent, descriptor)
	instructions := instructionsText != ""
	if len(agent.Spec.Tools.Pins) == 0 && agent.Spec.Model == nil && agent.Spec.Revision == "" && !ego && !instructions &&
		controlRoot == nil {
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
	config.Command = writeConfigCommand(descriptor.configMountPath, descriptor, ego, instructions, controlRoot != nil)
	config.Env = []corev1.EnvVar{{Name: configContentVariable, Value: file}}
	if ego {
		config.Env = append(config.Env, corev1.EnvVar{Name: egoContentVariable, Value: egoText})
	}
	if instructions {
		config.Env = append(config.Env, corev1.EnvVar{Name: instructionsContentVariable, Value: instructionsText})
	}
	config.SecurityContext = containerSecurityContext()
	config.VolumeMounts = []corev1.VolumeMount{{Name: configVolumeName, MountPath: descriptor.configMountPath}}

	// The control root is public, and the manager already reads it, so it takes
	// the config file's road into the Pod: a variable on this container alone,
	// written into a volume of its own that only the adapter mounts (ADR 0049).
	if controlRoot != nil {
		config.Env = append(config.Env, corev1.EnvVar{Name: controlRootContentVariable, Value: string(controlRoot)})
		config.VolumeMounts = append(config.VolumeMounts,
			corev1.VolumeMount{Name: controlRootVolumeName, MountPath: controlRootMountPath})
		statefulSet.Spec.Template.Spec.Volumes = append(statefulSet.Spec.Template.Spec.Volumes, corev1.Volume{
			Name:         controlRootVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	}

	// Read-only, because the agent reads these files and writes nothing back to
	// them. Only the agent mounts them: the instructions file is no other
	// container's, and sherlock does not mode-check it.
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
	// The workspace's own claim, at the path the agent holds its state claim at,
	// so the directory it serves keeps its path, and the agent's memory path does
	// not exist in this container (ADR 0044). The state claim is not mounted
	// here: what the workspace runs is the agent's untrusted code. Nor is the
	// credential's copy — the workspace reads no credential, and every container
	// mounting it is one more that can.
	//
	// A StatefulSet kept in the shared shape has no workspace claim, and its
	// workspace keeps the mount it had, on the state claim: its claim templates
	// cannot change, and a mount naming a claim it lacks would be refused
	// (ADR 0047). The Agent reports that shape as not isolated.
	claim := workspaceVolumeName
	if !hasWorkspaceClaim(statefulSet) {
		claim = stateVolumeName
	}
	workspace.VolumeMounts = []corev1.VolumeMount{{Name: claim, MountPath: descriptor.stateMountPath}}
}

// applySeed builds, on a StatefulSet replacing the shared shape, the init
// container that copies the workspace off the state claim onto the workspace's
// own, and builds none on any other. It runs before the agent and the workspace
// start, runs the operator's script alone and never anything the agent runs, and
// reads the state claim read-only. It stays for the StatefulSet's life: after
// the first copy the marker makes it a no-op, and taking it out would roll the
// Pod a second time for nothing (ADR 0044).
func applySeed(copyImage string, statefulSet *appsv1.StatefulSet) {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	if statefulSet.Annotations[seedAnnotation] == "" {
		*initContainers = slices.DeleteFunc(*initContainers, func(initContainer corev1.Container) bool {
			return initContainer.Name == seedContainerName
		})

		return
	}

	seed := containerNamed(initContainers, seedContainerName)
	seed.Image = copyImage
	// Always, on the ground the credential's init container carries.
	seed.ImagePullPolicy = corev1.PullAlways
	seed.Command = seedWorkspaceCommand(seedStateMountPath+"/"+workspaceDirName,
		seedWorkspaceMountPath+"/"+workspaceDirName, seedWorkspaceMountPath+"/"+seededMarker)
	seed.SecurityContext = containerSecurityContext()
	seed.VolumeMounts = []corev1.VolumeMount{
		{Name: stateVolumeName, MountPath: seedStateMountPath, ReadOnly: true},
		{Name: workspaceVolumeName, MountPath: seedWorkspaceMountPath},
	}
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
		{Name: adapterAgentSetting, Value: grn},
		{Name: adapterMachineURLSetting, Value: "https://" + r.GaramAddress},
		{Name: adapterGatewayURLSetting, Value: "http://" + descriptor.gatewayAddress},
		{Name: adapterCertFileSetting, Value: adapterCredentialsMountPath + "/" + garam.CertificateKey},
		{Name: adapterKeyFileSetting, Value: adapterCredentialsMountPath + "/" + garam.KeyKey},
		{Name: adapterServerRootSetting, Value: adapterCredentialsMountPath + "/" + garam.ServerRootKey},
	}
	adapter.VolumeMounts = adapterVolumeMounts()
	if !r.adapterFenced(agent) {
		// Legacy and unfenced: garam@fdfb76d's adapter refuses to start without
		// this (internal/cli/delivery.go:90-91), and garam@e81a1e0's reads it
		// nowhere, so it is kept for every image this mode runs with (ADR 0049).
		adapter.Env = append(adapter.Env, corev1.EnvVar{Name: adapterGatewayAgentSetting, Value: grn})

		return
	}

	// Fenced (garam@e81a1e0, ADR-0084): the three control settings together, and
	// the outbox beside them, which that adapter requires
	// (garam@e81a1e0:internal/cli/cli.go:79-91,188-191;
	// internal/cli/delivery.go:94-109).
	adapter.Env = append(adapter.Env,
		corev1.EnvVar{Name: adapterControlURLSetting, Value: "https://" + r.ControlAddress},
		corev1.EnvVar{Name: adapterControlRootSetting, Value: controlRootMountPath + "/" + controlRootFile},
		corev1.EnvVar{Name: adapterPlacementTokenSetting, Value: placementMountPath + "/" + placementTokenKey},
		corev1.EnvVar{Name: adapterOutboxDirSetting, Value: outboxMountPath})
	adapter.VolumeMounts = append(adapter.VolumeMounts,
		corev1.VolumeMount{Name: controlRootVolumeName, MountPath: controlRootMountPath, ReadOnly: true},
		// The outbox and nothing else of the state claim: the adapter forwards
		// the entries and clears the ones garam acknowledged
		// (garam@e81a1e0:internal/delivery/outbox.go:142-157), so it writes, and
		// it never sees the memory store.
		corev1.VolumeMount{Name: stateVolumeName, MountPath: outboxMountPath, SubPath: descriptor.outboxDir()})
}

// adapterFenced reports whether the agent's adapter activates through the
// control service: only where the adapter is built, this operator is told to,
// and the agent is the control service's, since only those register a
// placement to activate (ADR 0049).
func (r *AgentReconciler) adapterFenced(agent *agentv1alpha1.Agent) bool {
	return r.adapterBuilt(agent) && r.AdapterControl &&
		agent.Spec.Identity.Source == agentv1alpha1.DesiredSourceControl
}

// controlRootFor is the control root to give the agent's adapter, nil where it
// is given none. It is read at each reconcile, so a rotated root reaches the
// next Pod.
func (r *AgentReconciler) controlRootFor(agent *agentv1alpha1.Agent) ([]byte, error) {
	if !r.adapterFenced(agent) {
		return nil, nil
	}
	root, err := os.ReadFile(r.ControlRootFile)
	if err != nil {
		return nil, fmt.Errorf("read the control root: %w", err)
	}

	return root, nil
}

// applyOutbox builds, where the adapter is fenced, the init container that
// makes the agent's outbox directory on the state claim as the Pod's user,
// before the adapter mounts it, and removes it elsewhere. It makes the directory
// only where it is absent and touches nothing else.
func (r *AgentReconciler) applyOutbox(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet,
	descriptor agentTypeDescriptor) {
	initContainers := &statefulSet.Spec.Template.Spec.InitContainers
	if !r.adapterFenced(agent) {
		*initContainers = slices.DeleteFunc(*initContainers, func(initContainer corev1.Container) bool {
			return initContainer.Name == outboxContainerName
		})

		return
	}

	outbox := containerNamed(initContainers, outboxContainerName)
	outbox.Image = r.CopyImage
	// Always, on the ground the credential's init container carries.
	outbox.ImagePullPolicy = corev1.PullAlways
	outbox.Command = makeOutboxCommand(outboxStateMountPath + "/" + descriptor.outboxDir())
	outbox.SecurityContext = containerSecurityContext()
	outbox.VolumeMounts = []corev1.VolumeMount{{Name: stateVolumeName, MountPath: outboxStateMountPath}}
}

// makeOutboxCommand makes dir, and its parent, where dir is absent, the
// directory itself at outboxDirMode.
func makeOutboxCommand(dir string) []string {
	return shellCommand(fmt.Sprintf("if [ ! -d %[1]s ]; then mkdir -p %[2]s && mkdir -m %[3]s %[1]s; fi",
		dir, path.Dir(dir), outboxDirMode))
}

// adapterVolumeMounts is what the adapter reads, and nothing the agent alone
// does: the copy of the agent's credential, read-only, because the adapter is
// the agent to garam and reads the same key file the agent renews. It owns that
// copy for the reason the agent does — every container runs as the Pod's user.
//
// It also mounts the copy of the placement token, which the adapter alone
// reads, and which it is told of only where it activates through the control
// service (ADR 0049); there it mounts the control root and the outbox too.
func adapterVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: credentialsVolumeName, MountPath: adapterCredentialsMountPath, ReadOnly: true},
		// The placement token, which only the adapter reads (ADR 0042).
		{Name: placementVolumeName, MountPath: placementMountPath, ReadOnly: true},
	}
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

// claim is the claim template called name, provisioned at size from the
// storage class the Agent names.
func claim(name string, size resource.Quantity, agent *agentv1alpha1.Agent) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
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
