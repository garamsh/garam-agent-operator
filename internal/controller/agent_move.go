package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// A move's record is kept on the claims, which outlive the Agent, and never in
// the Agent's status, which a reconcile does not read (ADR 0065): a claim the
// memory moved off names the claim it moved to, a claim names its last writer,
// and the claim a move moves to holds every step of it.
const (
	// movedToAnnotation names, on a claim the agent's memory moved off, the
	// claim it moved to. Followed from the StatefulSet's own claim, the chain
	// ends at the claim the memory is on.
	movedToAnnotation = "agent.garam.sh/moved-to"

	// lastWriterAnnotation records, on a state claim, the last Pod that wrote
	// it: running while the Pod runs, and its agent's exit once the fence
	// released it.
	lastWriterAnnotation = "agent.garam.sh/last-writer"

	// The move's record on the claim it moves to.
	moveIDAnnotation           = "agent.garam.sh/move-id"
	movePhaseAnnotation        = "agent.garam.sh/move-phase"
	moveReasonAnnotation       = "agent.garam.sh/move-reason"
	moveMessageAnnotation      = "agent.garam.sh/move-message"
	moveSourceAnnotation       = "agent.garam.sh/move-source"
	moveSourceUIDAnnotation    = "agent.garam.sh/move-source-uid"
	moveSourceWriterAnnotation = "agent.garam.sh/move-source-writer"
	moveDigestAnnotation       = "agent.garam.sh/move-digest"
	moveCopyStartedAnnotation  = "agent.garam.sh/move-copy-started"
	moveCheckStartedAnnotation = "agent.garam.sh/move-verify-started"

	// moveAgentLabel names the Agent on a move's claim and its Jobs.
	moveAgentLabel = "agent.garam.sh/agent"

	// jobNameLabel is the label the Job controller gives every Pod of a Job.
	jobNameLabel = "batch.kubernetes.io/job-name"
)

// The phases of a move, in order. A refused move stays refused.
const (
	movePhaseCreated  = "created"
	movePhaseCopied   = "copied"
	movePhaseVerified = "verified"
	movePhaseSwitched = "switched"
	movePhaseAccepted = "accepted"
	movePhaseRefused  = "refused"
)

// Where a move's Job mounts the claim it reads and the claim it writes.
const (
	moveSourceMountPath = "/run/garam/move/source"
	moveTargetMountPath = "/run/garam/move/target"
	moveContainerName   = "move"

	// The two steps a move runs a Job for.
	moveStepCopy     = "copy"
	moveStepVerify   = "verify"
	moveSourceVolume = "source"
	moveTargetVolume = "target"
)

// moveManifestScript prints the digest of the stored state under a memory
// directory, the way sherlock's snapshot procedure manifests it
// (sherlock@b3c05c2:deploy/runtime-fixture/snapshot.sh:27-57): one line per
// entry of the store and its outbox, its mode, owner and content hash, an
// absent one named absent, and anything not a regular file or directory named
// special.
const moveManifestScript = `set -eu
memory=$1 base=$2
say() { printf '%s' "$1" >/dev/termination-log; echo "$1" >&2; }
fail() { say "$1"; exit 1; }
entry() {
  if [ -L "$1" ]; then echo "special $1"
  elif [ -d "$1" ]; then echo "dir $1 $(stat -c '%a %u:%g' "$1")"
  elif [ -f "$1" ]; then echo "file $1 $(stat -c '%a %u:%g' "$1") $(sha256sum <"$1" | cut -d' ' -f1)"
  elif [ -e "$1" ]; then echo "special $1"
  else echo "absent $1"; fi
}
manifest() (
  cd "$1"
  for f in "$base" "$base-wal" "$base-shm"; do entry "$f"; done
  if [ -d outbox ] && [ ! -L outbox ]; then
    find outbox | LC_ALL=C sort | while IFS= read -r p; do entry "$p"; done
  else entry outbox; fi
)
digest() { manifest "$1" | sha256sum | cut -d' ' -f1; }
`

// moveCopyScript copies the stored state from the source claim into an empty
// target, holding the store's writer lock for the whole copy, and reports the
// source's digest taken before it copied.
const moveCopyScript = moveManifestScript + `src=` + moveSourceMountPath + `/$memory dst=` + moveTargetMountPath + `/$memory
[ -f "$src/$base" ] && [ ! -L "$src/$base" ] || fail "the source holds no memory store at $memory/$base"
if manifest "$src" | grep -q '^special '; then fail "the source holds an entry that is not a regular file or directory"; fi
if ls -A ` + moveTargetMountPath + ` | grep -qvxF lost+found; then fail "the target volume is not empty"; fi
if [ -e "$src/$base.lock" ]; then
  exec 9<"$src/$base.lock"
  flock -n -x 9 || fail "the store's writer lock is held"
fi
before=$(digest "$src")
mkdir "$dst"
for f in "$base" "$base-wal" "$base-shm"; do
  if [ -f "$src/$f" ]; then cp -p "$src/$f" "$dst/$f"; fi
done
if [ -d "$src/outbox" ]; then cp -Rp "$src/outbox" "$dst/outbox"; fi
sync
say "source=$before"
`

// moveVerifyScript reads both claims and reports the digest of each, and how
// many entries the target holds beyond the copy.
const moveVerifyScript = moveManifestScript + `src=` + moveSourceMountPath + `/$memory dst=` + moveTargetMountPath + `/$memory
[ -d "$dst" ] || fail "the target holds no copy at $memory"
extra=$(ls -A "$dst" | grep -cvxF -e "$base" -e "$base-wal" -e "$base-shm" -e outbox || true)
outside=$(ls -A ` + moveTargetMountPath + ` | grep -cvxF -e "$memory" -e lost+found || true)
say "source=$(digest "$src") target=$(digest "$dst") extra=$((extra + outside))"
`

// statePlan is what the workload is built with for the agent's state: the
// claim its state volume mounts, whether the agent must find a store there, and
// whether a move holds it stopped.
type statePlan struct {
	// claim is the claim the state volume mounts by name. Empty is the
	// StatefulSet's own claim template.
	claim string

	// requireStore starts the agent refusing a store it cannot open, which is
	// how it accepts a copy.
	requireStore bool

	// hold keeps the workload at no replica.
	hold bool
}

// lastWriter is the record a state claim carries of its last writer.
type lastWriter struct {
	PodUID   string `json:"podUID"`
	State    string `json:"state"`
	ExitCode *int32 `json:"exitCode,omitempty"`
}

// The states a lastWriter records.
const (
	writerRunning = "running"
	writerStopped = "stopped"
)

// moveRefusal is a move refused, with the reason the condition reports.
type moveRefusal struct{ reason, message string }

// targetClaimName is the claim a move moves the agent's memory to.
func targetClaimName(agent *agentv1alpha1.Agent, id string) string {
	return agent.Name + "-state-" + id
}

// moveJobName names a move's Job by a hash of the Agent and the move, because a
// Job's name has to fit the label its Pods carry it in.
func moveJobName(agent *agentv1alpha1.Agent, id, step string) string {
	sum := sha256.Sum256([]byte(agent.Namespace + "/" + agent.Name + "/" + id))

	return "memory-move-" + hex.EncodeToString(sum[:8]) + "-" + step
}

// reconcileMove decides the claim the agent's state is on and takes a move of
// it one step further. Every step is decided again from the cluster's objects,
// so a manager stopped between two steps resumes at the step recorded.
func (r *AgentReconciler) reconcileMove(ctx context.Context, agent *agentv1alpha1.Agent,
	podGone bool, descriptor agentTypeDescriptor) (statePlan, error) {
	plan, inUse, err := r.stateInUse(ctx, agent)
	if err != nil || plan.hold {
		return plan, err
	}

	// A Job of a move still running reads the source, so nothing writes it
	// until the Job ends, whatever the spec now asks.
	running, err := r.moveJobRunning(ctx, agent)
	if err != nil {
		return plan, err
	}
	plan.hold = running

	move := agent.Spec.MemoryMove
	if move == nil {
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonNoMove,
			fmt.Sprintf("No move is asked for; the agent's memory is on claim %q", inUse))

		return plan, nil
	}

	target := &corev1.PersistentVolumeClaim{}
	targetName := targetClaimName(agent, move.ID)
	err = r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: targetName}, target)
	if apierrors.IsNotFound(err) {
		return r.beginMove(ctx, agent, plan, inUse, podGone)
	}
	if err != nil {
		return plan, fmt.Errorf("get claim %q: %w", targetName, err)
	}
	if target.Labels[moveAgentLabel] != agent.Name || target.Annotations[moveIDAnnotation] != move.ID {
		return r.refuseBeforeRecord(agent, plan, moveRefusal{agentv1alpha1.ReasonTargetForeign,
			fmt.Sprintf("Claim %q exists and no move of this Agent created it", targetName)})
	}

	if inUse == targetName {
		return r.settleMove(ctx, agent, plan, target)
	}

	return r.advanceMove(ctx, agent, plan, target, podGone, descriptor)
}

// stateInUse is the claim the agent's memory is on: the StatefulSet's own
// claim, or the end of the chain of moves that starts there. Every claim on the
// chain must be one a move of this Agent created; one that is not holds the
// agent stopped.
func (r *AgentReconciler) stateInUse(ctx context.Context, agent *agentv1alpha1.Agent) (statePlan, string, error) {
	name := stateClaimName(agent)
	for range maxMoves {
		claim := &corev1.PersistentVolumeClaim{}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: name}, claim)
		if apierrors.IsNotFound(err) && name == stateClaimName(agent) {
			return statePlan{}, name, nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return statePlan{}, "", fmt.Errorf("get claim %q: %w", name, err)
		}
		moved := err == nil && claim.Labels[moveAgentLabel] == agent.Name && claim.Annotations[moveIDAnnotation] != ""
		if name != stateClaimName(agent) && !moved {
			setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonSourceChanged,
				fmt.Sprintf("The agent's memory moved to claim %q, which does not exist or no move of this Agent created; "+
					"the agent is held at no replica", name))

			return statePlan{hold: true}, name, nil
		}
		next := claim.Annotations[movedToAnnotation]
		if next == "" {
			if name == stateClaimName(agent) {
				return statePlan{}, name, nil
			}

			return statePlan{claim: name, requireStore: true}, name, nil
		}
		name = next
	}
	setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonSourceChanged,
		fmt.Sprintf("The chain of claims the agent's memory moved through is longer than %d, so where it is cannot be read", maxMoves))

	return statePlan{hold: true}, name, nil
}

// maxMoves bounds the chain of moves stateInUse follows, so that claims edited
// into a cycle hold the agent rather than the reconcile.
const maxMoves = 64

// moveJobRunning reports whether a Job of a move of this Agent has not ended.
func (r *AgentReconciler) moveJobRunning(ctx context.Context, agent *agentv1alpha1.Agent) (bool, error) {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(agent.Namespace),
		client.MatchingLabels{moveAgentLabel: agent.Name}); err != nil {
		return false, fmt.Errorf("list the jobs moving the agent's memory: %w", err)
	}
	for i := range jobs.Items {
		outcome, err := r.jobOutcome(ctx, &jobs.Items[i])
		if err != nil {
			return false, err
		}
		if !outcome.ended {
			return true, nil
		}
	}

	return false, nil
}

// beginMove creates the claim a move copies into, once the agent is stopped and
// its last writer is seen to have drained cleanly. Nothing of the move exists
// before that, so a move refused here leaves nothing behind.
func (r *AgentReconciler) beginMove(ctx context.Context, agent *agentv1alpha1.Agent, plan statePlan,
	inUse string, podGone bool) (statePlan, error) {
	move := agent.Spec.MemoryMove
	if r.CopyImage == "" {
		return r.refuseBeforeRecord(agent, plan, moveRefusal{agentv1alpha1.ReasonCopyImageUnset,
			"This operator names no copy image (--agent-copy-image), so it cannot copy the agent's memory"})
	}

	statefulSet := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: agent.Name}, statefulSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return plan, fmt.Errorf("get statefulset: %w", err)
		}
	} else if !hasWorkspaceClaim(statefulSet) {
		return r.refuseBeforeRecord(agent, plan, moveRefusal{agentv1alpha1.ReasonMoveSharedClaim,
			"The workspace shares the state claim, so the claim holds the workspace as well as the memory; " +
				"it is moved only once the agent claims them separately (ADR 0047)"})
	}

	plan.hold = true
	if !podGone {
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveStopping,
			fmt.Sprintf("Stopping the agent to move its memory to claim %q; its Pod is released only on its writers' evidence",
				targetClaimName(agent, move.ID)))

		return plan, nil
	}

	source := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: inUse}, source); err != nil {
		if apierrors.IsNotFound(err) {
			return r.refuseBeforeRecord(agent, plan, moveRefusal{agentv1alpha1.ReasonSourceChanged,
				fmt.Sprintf("Claim %q, which the agent's memory is on, does not exist, so there is no memory to move", inUse)})
		}

		return plan, fmt.Errorf("get claim %q: %w", inUse, err)
	}
	if refusal := drainedCleanly(source); refusal != nil {
		return r.refuseBeforeRecord(agent, plan, *refusal)
	}

	target := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetClaimName(agent, move.ID),
			Namespace: agent.Namespace,
			Labels:    map[string]string{moveAgentLabel: agent.Name},
			Annotations: map[string]string{
				moveIDAnnotation:           move.ID,
				movePhaseAnnotation:        movePhaseCreated,
				moveSourceAnnotation:       source.Name,
				moveSourceUIDAnnotation:    string(source.UID),
				moveSourceWriterAnnotation: source.Annotations[lastWriterAnnotation],
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: move.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: move.StorageSize},
			},
		},
	}
	// No owner: the claim holds the agent's memory, which outlives the Agent as
	// its StatefulSet's claims do.
	if err := r.Create(ctx, target); err != nil && !apierrors.IsAlreadyExists(err) {
		return plan, fmt.Errorf("create claim %q: %w", target.Name, err)
	}
	logf.FromContext(ctx).Info("Created the claim the agent's memory moves to", "claim", target.Name, "source", source.Name)
	setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveCopying,
		fmt.Sprintf("Copying the agent's memory from claim %q to claim %q", source.Name, target.Name))

	return plan, nil
}

// drainedCleanly refuses a source whose last writer was not seen to exit 0 after
// its drain: sherlock takes a consistent copy only from a writer positively
// stopped (sherlock@b3c05c2:docs/architecture/deployment.md:265).
func drainedCleanly(source *corev1.PersistentVolumeClaim) *moveRefusal {
	recorded, ok := source.Annotations[lastWriterAnnotation]
	if !ok {
		return &moveRefusal{agentv1alpha1.ReasonWriterNotDrained,
			fmt.Sprintf("Claim %q carries no record of its last writer, so no clean exit was seen", source.Name)}
	}
	var writer lastWriter
	if err := json.Unmarshal([]byte(recorded), &writer); err != nil {
		return &moveRefusal{agentv1alpha1.ReasonWriterNotDrained,
			fmt.Sprintf("Claim %q's record of its last writer cannot be read: %v", source.Name, err)}
	}
	switch {
	case writer.State != writerStopped:
		return &moveRefusal{agentv1alpha1.ReasonWriterNotDrained,
			fmt.Sprintf("Claim %q's last writer, Pod %s, was never seen to stop; a Pod released by hand leaves no evidence", source.Name, writer.PodUID)}
	case writer.ExitCode == nil:
		return &moveRefusal{agentv1alpha1.ReasonWriterNotDrained,
			fmt.Sprintf("Claim %q's last writer, Pod %s, stopped with no agent exit observed", source.Name, writer.PodUID)}
	case *writer.ExitCode != 0:
		return &moveRefusal{agentv1alpha1.ReasonWriterNotDrained,
			fmt.Sprintf("Claim %q's last writer, Pod %s, exited %d rather than 0 after a drain; "+
				"let the agent run and stop cleanly once, then name a new move id", source.Name, writer.PodUID, *writer.ExitCode)}
	}

	return nil
}

// advanceMove takes a move whose claim exists, and is not yet the one the
// agent's memory is on, one step further: copy, verify, then switch.
func (r *AgentReconciler) advanceMove(ctx context.Context, agent *agentv1alpha1.Agent, plan statePlan,
	target *corev1.PersistentVolumeClaim, podGone bool, descriptor agentTypeDescriptor) (statePlan, error) {
	plan.hold = true
	phase := target.Annotations[movePhaseAnnotation]
	switch phase {
	case movePhaseRefused:
		setMemoryMove(agent, metav1.ConditionFalse, target.Annotations[moveReasonAnnotation],
			fmt.Sprintf("Move %q was refused, and its claim %q is kept and never used again: %s. The agent stays stopped "+
				"on claim %q until the move is cleared or a new id is named",
				agent.Spec.MemoryMove.ID, target.Name, target.Annotations[moveMessageAnnotation], target.Annotations[moveSourceAnnotation]))

		return plan, nil
	case movePhaseSwitched, movePhaseAccepted:
		return r.refuseBeforeRecord(agent, plan, moveRefusal{agentv1alpha1.ReasonTargetForeign,
			fmt.Sprintf("Claim %q was already moved to and is not the claim the agent's memory is on; name a new move id", target.Name)})
	}

	if !podGone {
		// Nothing runs on the source while a move holds the agent; a Pod here
		// is one still being released.
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveStopping,
			fmt.Sprintf("Waiting for the agent's Pod to be released before the move to claim %q continues", target.Name))

		return plan, nil
	}
	if refusal, err := r.sourceUnchanged(ctx, agent, target); err != nil || refusal != nil {
		if err != nil {
			return plan, err
		}

		return plan, r.refuseMove(ctx, agent, target, *refusal)
	}

	switch phase {
	case movePhaseCreated:
		return plan, r.runCopy(ctx, agent, target, descriptor)
	case movePhaseCopied:
		return plan, r.runVerify(ctx, agent, target, descriptor)
	case movePhaseVerified:
		return r.switchMove(ctx, agent, target)
	}

	return plan, r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonTargetForeign,
		fmt.Sprintf("Claim %q records move phase %q, which no move writes", target.Name, phase)})
}

// sourceUnchanged refuses a move whose source is no longer the claim it was
// begun on, or has had a writer since.
func (r *AgentReconciler) sourceUnchanged(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim) (*moveRefusal, error) {
	name := target.Annotations[moveSourceAnnotation]
	source := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: name}, source); err != nil {
		if apierrors.IsNotFound(err) {
			return &moveRefusal{agentv1alpha1.ReasonSourceChanged, fmt.Sprintf("Source claim %q no longer exists", name)}, nil
		}

		return nil, fmt.Errorf("get claim %q: %w", name, err)
	}
	if string(source.UID) != target.Annotations[moveSourceUIDAnnotation] {
		return &moveRefusal{agentv1alpha1.ReasonSourceChanged,
			fmt.Sprintf("Source claim %q is %s, not the claim the move began on", name, source.UID)}, nil
	}
	if source.Annotations[lastWriterAnnotation] != target.Annotations[moveSourceWriterAnnotation] {
		return &moveRefusal{agentv1alpha1.ReasonSourceChanged,
			fmt.Sprintf("Source claim %q has had a writer since the move began, so the copy is not its state", name)}, nil
	}

	return nil, nil
}

// runCopy runs the copy Job and records its outcome.
func (r *AgentReconciler) runCopy(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim, descriptor agentTypeDescriptor) error {
	outcome, err := r.ensureMoveJob(ctx, agent, target, descriptor, moveStepCopy, moveCopyStartedAnnotation)
	if err != nil {
		return err
	}
	switch {
	case outcome.refusal != nil:
		return r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonCopyFailed, outcome.refusal.message})
	case !outcome.ended:
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveCopying,
			fmt.Sprintf("Copying the agent's memory from claim %q to claim %q",
				target.Annotations[moveSourceAnnotation], target.Name))

		return nil
	}

	reported := reportedDigests(outcome.message)
	if reported["source"] == "" {
		return r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonCopyFailed,
			fmt.Sprintf("The copy reported no digest of its source: %q", outcome.message)})
	}

	return r.recordMovePhase(ctx, agent, target, movePhaseCopied, map[string]string{moveDigestAnnotation: reported["source"]},
		agentv1alpha1.ReasonMoveVerifying, fmt.Sprintf("Verifying the copy on claim %q against its source", target.Name))
}

// runVerify runs the verification Job and compares what it read: the source's
// digest now, the source's digest when the copy began, and the copy's digest
// must be one value, and the copy must hold nothing else.
func (r *AgentReconciler) runVerify(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim, descriptor agentTypeDescriptor) error {
	outcome, err := r.ensureMoveJob(ctx, agent, target, descriptor, moveStepVerify, moveCheckStartedAnnotation)
	if err != nil {
		return err
	}
	switch {
	case outcome.refusal != nil:
		return r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonVerificationFailed, outcome.refusal.message})
	case !outcome.ended:
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveVerifying,
			fmt.Sprintf("Verifying the copy on claim %q against its source", target.Name))

		return nil
	}

	reported := reportedDigests(outcome.message)
	copied := target.Annotations[moveDigestAnnotation]
	var mismatch string
	switch {
	case reported["source"] == "" || reported["target"] == "" || reported["extra"] == "":
		mismatch = fmt.Sprintf("the verification reported no digests: %q", outcome.message)
	case reported["source"] != copied:
		mismatch = fmt.Sprintf("the source changed while it was copied: %s before, %s after", copied, reported["source"])
	case reported["target"] != reported["source"]:
		mismatch = fmt.Sprintf("the copy's digest %s does not match its source's %s", reported["target"], reported["source"])
	case reported["extra"] != "0":
		mismatch = fmt.Sprintf("the target holds %s entries beyond the copy", reported["extra"])
	}
	if mismatch != "" {
		return r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonVerificationFailed, mismatch})
	}

	return r.recordMovePhase(ctx, agent, target, movePhaseVerified, nil, agentv1alpha1.ReasonMoveStarting,
		fmt.Sprintf("The copy on claim %q matches its source, digest %s", target.Name, copied))
}

// switchMove makes the verified copy the claim the agent's memory is on, by
// naming it on the source. That is written first, so a manager stopped before
// the phase is recorded finds the copy in use and records it then.
func (r *AgentReconciler) switchMove(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim) (statePlan, error) {
	if err := r.linkMove(ctx, agent, target.Annotations[moveSourceAnnotation], target.Name); err != nil {
		return statePlan{hold: true}, err
	}

	return r.settleMove(ctx, agent, statePlan{claim: target.Name, requireStore: true}, target)
}

// settleMove carries a move whose copy the agent's memory is on to its end: the
// first Pod on the copy serving accepts it, and one whose agent exits first
// refuses it and puts the memory back on the source.
func (r *AgentReconciler) settleMove(ctx context.Context, agent *agentv1alpha1.Agent, plan statePlan,
	target *corev1.PersistentVolumeClaim) (statePlan, error) {
	switch target.Annotations[movePhaseAnnotation] {
	case movePhaseVerified:
		if err := r.recordMovePhase(ctx, agent, target, movePhaseSwitched, nil, agentv1alpha1.ReasonMoveStarting,
			fmt.Sprintf("Starting the agent on claim %q; it must accept the copy before it serves", target.Name)); err != nil {
			return plan, err
		}
	case movePhaseSwitched:
	case movePhaseAccepted:
		setMemoryMove(agent, metav1.ConditionTrue, agentv1alpha1.ReasonMoveAccepted,
			fmt.Sprintf("The agent accepted the copy on claim %q; claim %q is kept, and deleting it is a person's step",
				target.Name, target.Annotations[moveSourceAnnotation]))

		return plan, nil
	case movePhaseRefused:
		// Refused after the switch, by a manager stopped before it put the
		// memory back on the source.
		return r.restoreSource(ctx, agent, target)
	default:
		return statePlan{hold: true}, r.refuseMove(ctx, agent, target, moveRefusal{agentv1alpha1.ReasonSourceChanged,
			fmt.Sprintf("The agent's memory is on claim %q, whose move phase %q is not one a switch follows",
				target.Name, target.Annotations[movePhaseAnnotation])})
	}

	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: agentPodName(agent)}, pod)
	if apierrors.IsNotFound(err) || (err == nil && podStateClaim(pod) != target.Name) {
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveStarting,
			fmt.Sprintf("Starting the agent on claim %q; it must accept the copy before it serves", target.Name))

		return plan, nil
	}
	if err != nil {
		return plan, fmt.Errorf("get the agent's pod: %w", err)
	}

	if code, exited := agentExitedNonZero(pod); exited {
		refusal := moveRefusal{agentv1alpha1.ReasonAgentRefusedStore,
			fmt.Sprintf("the agent exited %d on the copy before it served; a move needs agent v0.2.0 or later, "+
				"which accepts --require-store", code)}
		if err := r.refuseMove(ctx, agent, target, refusal); err != nil {
			return plan, err
		}

		return r.restoreSource(ctx, agent, target)
	}
	if !podReady(pod) {
		setMemoryMove(agent, metav1.ConditionFalse, agentv1alpha1.ReasonMoveStarting,
			fmt.Sprintf("Starting the agent on claim %q; it must accept the copy before it serves", target.Name))

		return plan, nil
	}

	return plan, r.recordMovePhase(ctx, agent, target, movePhaseAccepted, nil, agentv1alpha1.ReasonMoveAccepted,
		fmt.Sprintf("The agent accepted the copy on claim %q; claim %q is kept, and deleting it is a person's step",
			target.Name, target.Annotations[moveSourceAnnotation]))
}

// restoreSource puts the agent's memory back on the claim a refused move was
// copied from, and holds the agent stopped there. A source other than the
// StatefulSet's own claim is one an earlier move made, which the agent already
// accepted under --require-store.
func (r *AgentReconciler) restoreSource(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim) (statePlan, error) {
	source := target.Annotations[moveSourceAnnotation]
	plan := statePlan{claim: source, requireStore: source != stateClaimName(agent), hold: true}

	return plan, r.linkMove(ctx, agent, source, "")
}

// agentExitedNonZero reports the exit of the agent's container, where it last
// terminated with one other than 0.
func agentExitedNonZero(pod *corev1.Pod) (int32, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != agentContainerName {
			continue
		}
		for _, state := range []corev1.ContainerState{status.State, status.LastTerminationState} {
			if state.Terminated != nil && state.Terminated.ExitCode != 0 {
				return state.Terminated.ExitCode, true
			}
		}
	}

	return 0, false
}

// podReady reports whether the Pod and its agent's container are ready.
func podReady(pod *corev1.Pod) bool {
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == agentContainerName {
			return ready && status.Ready
		}
	}

	return false
}

// podStateClaim is the claim the Pod's state volume mounts.
func podStateClaim(pod *corev1.Pod) string {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == stateVolumeName && volume.PersistentVolumeClaim != nil {
			return volume.PersistentVolumeClaim.ClaimName
		}
	}

	return ""
}

// jobOutcome is what a move's Job came to.
type jobOutcome struct {
	ended   bool
	message string
	refusal *moveRefusal
}

// ensureMoveJob creates the move's Job for step where it has not been, and
// reads its outcome where it has. The target is marked before the Job is made,
// so a Job found gone after that mark is one whose work is unknown.
func (r *AgentReconciler) ensureMoveJob(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim, descriptor agentTypeDescriptor, step, startedAnnotation string) (jobOutcome, error) {
	job := &batchv1.Job{}
	name := moveJobName(agent, agent.Spec.MemoryMove.ID, step)
	err := r.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: name}, job)
	if err == nil {
		return r.jobOutcome(ctx, job)
	}
	if !apierrors.IsNotFound(err) {
		return jobOutcome{}, fmt.Errorf("get job %q: %w", name, err)
	}
	if target.Annotations[startedAnnotation] != "" {
		return jobOutcome{ended: true, refusal: &moveRefusal{message: fmt.Sprintf(
			"the %s Job %q is gone with no outcome recorded, so what it did is unknown", step, name)}}, nil
	}

	marked := target.DeepCopy()
	marked.Annotations[startedAnnotation] = name
	if err := r.Patch(ctx, marked, client.MergeFrom(target)); err != nil {
		return jobOutcome{}, fmt.Errorf("mark claim %q before its %s Job: %w", target.Name, step, err)
	}
	*target = *marked

	job = r.moveJob(agent, target, descriptor, name, step)
	if err := controllerutil.SetControllerReference(agent, job, r.Scheme); err != nil {
		return jobOutcome{}, fmt.Errorf("own job %q: %w", name, err)
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return jobOutcome{}, fmt.Errorf("create job %q: %w", name, err)
	}
	logf.FromContext(ctx).Info("Started a step of the agent's memory move", "job", name, "step", step)

	return jobOutcome{}, nil
}

// jobOutcome reads a move's Job by its Pod's terminated container: exit 0 with
// the message it wrote is its result, any other exit refuses with that message,
// and a Job its controller failed with no Pod left to read refuses too.
func (r *AgentReconciler) jobOutcome(ctx context.Context, job *batchv1.Job) (jobOutcome, error) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(job.Namespace), client.MatchingLabels{jobNameLabel: job.Name}); err != nil {
		return jobOutcome{}, fmt.Errorf("list the pods of job %q: %w", job.Name, err)
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			terminated := status.State.Terminated
			if status.Name != moveContainerName || terminated == nil {
				continue
			}
			if terminated.ExitCode != 0 {
				return jobOutcome{ended: true, refusal: &moveRefusal{message: fmt.Sprintf(
					"Job %q exited %d: %s", job.Name, terminated.ExitCode, strings.TrimSpace(terminated.Message))}}, nil
			}

			return jobOutcome{ended: true, message: terminated.Message}, nil
		}
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return jobOutcome{ended: true, refusal: &moveRefusal{message: fmt.Sprintf(
				"Job %q failed with no Pod left to read: %s", job.Name, condition.Message)}}, nil
		}
	}

	return jobOutcome{}, nil
}

// moveJob is the Job that runs step of a move: the copy reads the source and
// writes the target, and the verification reads both. It runs the copy image as
// the agent's own user, with no service account token, once.
func (r *AgentReconciler) moveJob(agent *agentv1alpha1.Agent, target *corev1.PersistentVolumeClaim,
	descriptor agentTypeDescriptor, name, step string) *batchv1.Job {
	script := moveCopyScript
	if step != moveStepCopy {
		script = moveVerifyScript
	}
	labels := map[string]string{
		moveAgentLabel:               agent.Name,
		"app.kubernetes.io/name":     "agent-memory-move",
		"app.kubernetes.io/instance": agent.Name,
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To[int32](0),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup:        ptr.To[int64](agentFSGroup),
						RunAsUser:      ptr.To[int64](agentRunAsUser),
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            moveContainerName,
						Image:           r.CopyImage,
						ImagePullPolicy: corev1.PullAlways,
						Command: append(shellCommand(script), "move",
							path.Dir(descriptor.memoryFile), path.Base(descriptor.memoryFile)),
						SecurityContext: containerSecurityContext(),
						VolumeMounts: []corev1.VolumeMount{
							{Name: moveSourceVolume, MountPath: moveSourceMountPath, ReadOnly: true},
							{Name: moveTargetVolume, MountPath: moveTargetMountPath, ReadOnly: step != moveStepCopy},
						},
					}},
					Volumes: []corev1.Volume{
						claimVolume(moveSourceVolume, target.Annotations[moveSourceAnnotation], true),
						claimVolume(moveTargetVolume, target.Name, step != moveStepCopy),
					},
				},
			},
		},
	}
}

// claimVolume is a Pod volume mounting the claim called claim.
func claimVolume(name, claim string, readOnly bool) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: readOnly},
	}}
}

// reportedDigests reads the key=value fields a move's Job reports.
func reportedDigests(message string) map[string]string {
	fields := map[string]string{}
	for field := range strings.FieldsSeq(message) {
		if key, value, ok := strings.Cut(field, "="); ok {
			fields[key] = value
		}
	}

	return fields
}

// recordMovePhase advances the move's record on its claim, and reports it.
func (r *AgentReconciler) recordMovePhase(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim, phase string, extra map[string]string, reason, message string) error {
	marked := target.DeepCopy()
	marked.Annotations[movePhaseAnnotation] = phase
	maps.Copy(marked.Annotations, extra)
	if err := r.Patch(ctx, marked, client.MergeFrom(target)); err != nil {
		return fmt.Errorf("record move phase %q on claim %q: %w", phase, target.Name, err)
	}
	*target = *marked
	status := metav1.ConditionFalse
	if phase == movePhaseAccepted {
		status = metav1.ConditionTrue
	}
	setMemoryMove(agent, status, reason, message)
	logf.FromContext(ctx).Info("Recorded a step of the agent's memory move", "claim", target.Name, "phase", phase)

	return nil
}

// refuseMove records a move refused on its claim, which is kept and never used
// again, and reports it. The source is never written.
func (r *AgentReconciler) refuseMove(ctx context.Context, agent *agentv1alpha1.Agent,
	target *corev1.PersistentVolumeClaim, refusal moveRefusal) error {
	if err := r.recordMovePhase(ctx, agent, target, movePhaseRefused, map[string]string{
		moveReasonAnnotation: refusal.reason, moveMessageAnnotation: refusal.message,
	}, refusal.reason, ""); err != nil {
		return err
	}
	setMemoryMove(agent, metav1.ConditionFalse, refusal.reason,
		fmt.Sprintf("Move %q was refused, and its claim %q is kept and never used again: %s. The agent stays stopped on "+
			"claim %q until the move is cleared or a new id is named",
			agent.Spec.MemoryMove.ID, target.Name, refusal.message, target.Annotations[moveSourceAnnotation]))

	return nil
}

// refuseBeforeRecord reports a move refused before anything of it was
// recorded, holding the agent stopped while the spec asks for it.
func (r *AgentReconciler) refuseBeforeRecord(agent *agentv1alpha1.Agent, plan statePlan,
	refusal moveRefusal) (statePlan, error) {
	plan.hold = true
	setMemoryMove(agent, metav1.ConditionFalse, refusal.reason,
		refusal.message+". The agent stays stopped until the move is cleared or a new id is named")

	return plan, nil
}

// linkMove names on the source claim the claim the memory moved to, or, with
// to empty, takes that name off, which puts the memory back on the source.
func (r *AgentReconciler) linkMove(ctx context.Context, agent *agentv1alpha1.Agent, source, to string) error {
	claim := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: agent.Namespace, Name: source}, claim); err != nil {
		return fmt.Errorf("get claim %q: %w", source, err)
	}
	if claim.Annotations[movedToAnnotation] == to {
		return nil
	}
	marked := claim.DeepCopy()
	if marked.Annotations == nil {
		marked.Annotations = map[string]string{}
	}
	if to == "" {
		delete(marked.Annotations, movedToAnnotation)
	} else {
		marked.Annotations[movedToAnnotation] = to
	}
	if err := r.Patch(ctx, marked, client.MergeFrom(claim)); err != nil {
		return fmt.Errorf("name on claim %q where the memory moved: %w", source, err)
	}

	return nil
}
