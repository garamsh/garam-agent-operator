# ADR 0061: Release a deleted Pod whose writers were never created only on the kubelet's terminal phase, never on an empty container ID alone

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #311, from the `garamsh/garam#1196` run 6 acceptance (S8, order 1). A Pod deleted while its init containers were still waiting stayed held by `agent.garam.sh/writer-stopped` and was released by hand.

[ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md) counts a writer as stopped only when it reports `state.terminated` with a container ID, and counts "never started" only for a Pod with no `nodeName`. A scheduled Pod whose writers were never created shows neither, so it needed a hand release.

#311 proposed this evidence: each writer has an empty `containerID`, has never had a `lastState`, and is waiting. The PM asked first whether that is proof. The kubelet was read at the minimum supported version and the current one, `kubernetes/kubernetes` `v1.33.0` and `v1.37.1`.

**The empty container ID is not proof that a container never ran.** The API's container status is rebuilt on every sync from what the container runtime lists at that moment.
- `convertToAPIContainerStatuses` sets `containerID` from the runtime's own status (`pkg/kubelet/kubelet_pods.go` v1.33.0:2006,2024; v1.37.1:2323,2400).
- Every container the runtime does not return gets a fresh default waiting status with no ID (v1.33.0:2151, v1.37.1:2531).
- From the previous status it keeps only the restart count and the last termination state, and keeps the whole status only if that status was terminated.

A container that did run can therefore show exactly #311's shape:
- **(a) Stale status.** The kubelet created and started the container, and its status update, which is asynchronous, has not reached the API. Perhaps the kubelet is slow or hung, or partitioned while its node still reads Ready within the node-monitor grace period. Releasing on the empty ID would let a second writer start beside a running one. This case alone settles the question.
- **(b) A container gone from the runtime.** Container GC, a manual removal, or runtime state lost on a reboot removes the container, and the kubelet never posted it as running or terminated, for example because it started and died while the kubelet was down. The kubelet's recovery for a vanished container, `ContainerStatusUnknown` in the last state, fires only when the previous status was `Running` (v1.33.0:2220-2262). Otherwise the result is the default waiting status: no ID, no last state.
- **(c) A kubelet restart** that loses its status cache, together with (b).

**The kubelet's terminal phase is positive evidence.**
- `SyncTerminatingPod` kills the Pod, then asks the runtime again (`GetPodStatus`). If anything still runs it fails with "detected running containers after a successful KillPod, CRI violation". Only then does it write the status with `podIsTerminal=true` (`pkg/kubelet/kubelet.go` v1.33.0:2094-2190, the check at :2174 and the write at :2190; v1.37.1:2340-2452, :2436 and :2452).
- The status manager refuses a transition to `Failed` or `Succeeded` while the pod worker says containers could be running (`mergePodStatus`, `pkg/kubelet/status/status_manager.go` v1.33.0:1091-1145, at :1131; v1.37.1:1452, :1496).
- `CouldHaveRunningContainers` is true for a known Pod until it terminates, and for any Pod until the pod workers have synced after a restart (`pkg/kubelet/pod_workers.go` v1.33.0:632, v1.37.1:658).
- A deleted Pod is not started again.
- For a Pod stuck in init, `TerminatePod` leaves the regular containers waiting (`status_manager.go` v1.33.0:467-500), which is #311's shape.

**What else writes a terminal phase.** PodGC sets `Failed` on (`pkg/controller/podgc/gc_controller.go` v1.33.0:143-341, v1.37.1:148-346):
- a Pod whose node is gone (`gcOrphaned`), with `DisruptionTarget` reason `DeletionByPodGC`;
- a terminating Pod on a NotReady node with the `node.kubernetes.io/out-of-service` taint (`gcTerminating`), with no condition;
- an unscheduled terminating Pod (`gcUnscheduledTerminating`), with no condition.

`DisruptionTarget` is also set by:
- the taint eviction controller, `DeletionByTaintManager` (`pkg/controller/tainteviction/taint_eviction.go:136-139`, both versions);
- the eviction API, `EvictionByEvictionAPI` (`pkg/registry/core/pod/storage/eviction.go` v1.33.0:345-347, v1.37.1:346-348);
- the scheduler's preemption, `PreemptionByScheduler` (v1.33.0 `pkg/scheduler/framework/preemption/preemption.go:174-177`, v1.37.1 `executor.go:127-130`).

None of these is a kubelet-confirmed termination. The kubelet's own reason is `TerminationByKubelet`, from its eviction manager, its graceful node shutdown and its preemption. The status manager holds that condition back until the transition to a terminal phase with nothing running (`status_manager.go` v1.33.0:1108, v1.37.1:1469).

The PM settled the decision on this dispatch.

## Decision

**#311's candidate is refused as evidence.** An empty `containerID`, no last state and a waiting state do not show that a writer never ran, for (a), (b) and (c).

**A writer the kubelet shows never created counts as stopped only in a Pod the kubelet made terminal.** Every check of ADR 0042 stays. A writer with no terminated state counts as stopped, and is named in the evidence's `neverCreated`, only when all of these hold:
- the deleting Pod's `status.phase` is `Failed` or `Succeeded`;
- its node exists and is Ready `True`, which is stricter than ADR 0042's not-`Unknown`, and carries no `node.kubernetes.io/out-of-service` taint;
- the Pod carries no `DisruptionTarget` condition, or only one with reason `TerminationByKubelet`;
- the writer reports an empty `containerID`, a restart count of 0, no last state, and a waiting state or none;
- the claim check passes, as before.

**What the evidence records.** It carries `podPhase` as its basis, and `neverCreated` names each such writer. A writer that terminated is recorded under `containers`, as before.

**Everything else holds as before.** That covers a waiting writer with an ID, a restart or a last state, and a never-created writer in a Pod still `Pending` or `Running`, which is the stale-status window (a). It also covers a writer `terminated` with no ID. That is what `TerminatePod` makes of an initialized Pod's never-started container, and also what it makes of one that ran and vanished. It still needs a hand release.

## Consequences

- **A Pod deleted before any writer was created is released without a hand step.** That includes one stuck in init, or one whose volumes never mounted, once the kubelet marks it `Failed`.
- **The evidence is the kubelet's word.** It is read through the API as every other part of the fence is. A person with `patch` on Pod status can forge it, as they can forge a terminated container today.
- **A Pod whose node was declared out of service, or that PodGC failed, still needs a hand release**, unless its writers terminated with IDs.
- **`status.writerStopped` gains two optional fields**, `neverCreated` and `podPhase`. Both are omitted when empty, so the placement digest of every evidence recorded before this decision is unchanged.

Ruled out:
- **Accepting #311's candidate on its own**, for (a), (b) and (c).
- **Accepting any terminal phase.** PodGC writes one for a Pod whose node is gone or out of service, where nothing confirms the containers stopped.
