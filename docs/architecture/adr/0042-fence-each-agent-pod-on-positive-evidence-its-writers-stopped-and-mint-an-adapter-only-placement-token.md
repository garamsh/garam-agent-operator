# ADR 0042: Fence each agent Pod on positive evidence that its writers stopped, and mint an adapter-only placement token per placement

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #212, first slice: the manager side of the writer fence and of the placement token. An agent's state is a single-writer store on one volume (ADR 0005), and the StatefulSet creates the next Pod on that volume once the old Pod object is gone. Kubernetes force deletion removes the object without waiting for the kubelet, so a Pod that is gone is no evidence that its containers stopped. A replacement could start writing beside a writer still running on a partitioned node.

#212 agreed the fence and the token with Garam's PM in run:run_f54ec1eca8c3, and its comments bind:

- **Evidence must be positive.** "Never started" counts only for a deleting Pod never assigned to a node. Empty or missing container statuses are not evidence.
- **Finalizer removal is never itself the evidence.** A release without evidence is a deliberate, recorded human act and never a timeout.
- **Persist first.** The evidence record, not only a digest, is written to the `Agent`'s status before the finalizer is removed.
- **Same-Pod restarts.** A container restarting in the same Pod is not a new placement.
- **Sherlock's drain.** At `sherlock@2ad4c13` (#940), the drain releases the writer lock last, so evidence can rest on the agent container's termination after SIGTERM.

The PM settled the bindings #212 left open on this dispatch, recorded on #212.

Out of this slice:

- registering a placement on the control service's route (#218 builds the route);
- re-presenting placements when the manager's leaf renews (that needs the route);
- the control root and URL mounts;
- the adapter's setting naming the token file, because garam#1169 has not published that name.

## Decision

**Every agent Pod carries the finalizer `agent.garam.sh/writer-stopped`**, set on the StatefulSet's Pod template so the StatefulSet controller copies it to every Pod it creates. The manager watches the agent's Pod and acts on it in the `Agent`'s reconcile, the lifecycle domain (`internal/controller`).

**A deleting Pod is released only on positive evidence.** The checks run in order, and the first that fails holds the Pod with `WriterFence` at status `Unknown`. The message reads "Unverified", and the reason is one of these:

- `NodeUnknown`: the Pod's node is gone, reports no Ready condition, or reports Ready `Unknown`. The node is read uncached through the manager's API reader, so no informer covers every node; RBAC is `get` on nodes only.
- `PVCChanged`: the state volume's claim is missing, or is not the claim the Pod started on. See below.
- `ContainerRunning` or `ContainerWaiting`: a writing container is in that state. A waiting state reported by a scheduled Pod's node is not taken to mean "never started".
- `NoContainerStatus`: a writing container reports no status or no terminated container ID.

The writing containers are every regular container (the agent and the workspace) and every init container with `restartPolicy: Always` (the adapter). Run-to-completion init containers hold nothing of the store. A Pod whose `spec.nodeName` is empty is released as never started, with an empty container list.

**The release is ordered, and each step is safe to repeat:**

1. The evidence `{podUID, pvcUID, containers: [{name, containerID, exitCode, finishedAt}], observedAt}` is written to `status.writerStopped` with its own status patch.
2. Where the adapter is placed, the next placement token is minted.
3. The finalizer is removed.

A manager stopped between steps resumes by reading the Pod again: it is still deleting and still terminated, so the same evidence is computed and written again. Nothing can start on the volume until step 3. `WriterFence` becomes `True`, reason `WriterStopped`.

**An unverified Pod is read again every 30 seconds and is never released on a timeout.** Pod changes arrive through the watch. A node that recovers is noticed only by looking again.

**Where the claim's baseline lives.** The first time the manager sees a Pod that is not deleting, it records the claim's UID on that Pod as the annotation `agent.garam.sh/pvc-uid`, and it never replaces a value already there.

- On deletion, the claim counts as changed if the annotation is missing or different.
- It also counts as changed if the claim's `creationTimestamp` is after the Pod's. The StatefulSet creates the claim before the Pod, so the Pod's own claim is never newer than it.
- The annotation is fence evidence, kept on the object the fence holds. It is not kept in the `Agent`'s status, because `stack-kubebuilder.md` §3 keeps the reconciler from reading status as an input. `status.placement {podUID, pvcUID}` and `status.writerStopped` are reports and are never read back.

**What a hand edit can and cannot do:**

- A hand-edited wrong annotation makes the fence stricter: the Pod is held as `PVCChanged`.
- An annotation edited to match a recreated claim does not release the Pod, because the creation times cannot be edited.
- The window left is exactly this: a claim recreated within the same second as the Pod's creation, masked by an annotation edit, by someone who already holds `patch` on Pods in the namespace.

**Every `Agent` carries the finalizer `agent.garam.sh/workload-fenced`**, added before its workload is built. This supersedes `agent.md`'s statement that the reconciler holds no finalizer. A deleting `Agent` is taken down in order:

1. The manager deletes its StatefulSet, because the `Agent`'s own finalizer stops the garbage collector from deleting what it owns.
2. The StatefulSet's Pod is fenced as above, and its evidence is recorded on the still-present `Agent`.
3. The `Agent`'s finalizer comes off once the Pod is gone, which also covers an `Agent` that never had a Pod.

While the Pod is unverified, the `Agent` stays deleting until a person releases the Pod. That is deliberate. Meanwhile the poller builds no second `Agent` under the same name, because the name is still taken.

**The placement token.** A placement is a Pod: a new Pod is a new placement, and a container restart in the same Pod is not.

- **Minting:** 32 random bytes, hex-encoded, held under the key `token` in the Secret `<agent>-placement`, which the `Agent` owns. It is minted when the Secret is absent, before the first Pod, and minted again at each fence release, before the next Pod is created.
- **Delivery:** the same init container that copies the agent's credential (ADR 0010) copies the token from the Secret's projection into a memory volume at mode 0600, owned by the Pod's user. Only the adapter mounts that copy, read-only, at `/run/garam/placement/token`, in ADR 0034's seam. That is the strictest rule Garam applies to a file it reads, so the delivery does not change if garam#1169 applies its key-file rule to the token.
- **When it exists:** the Secret and its mounts exist only where the adapter is placed, since only the adapter reads them.
- **No setting yet:** no `GARAM_ADAPTER_*` variable names the token's file. garam#1169 has not published one, and the slice that binds #1169's commit sets it.

## Consequences

- **A replacement Pod waits for its predecessor's writers.** Force-deleting a running Pod no longer frees the volume for a new writer: the StatefulSet sees the old object until the kubelet reports every writer terminated, or a person releases it.
- **A Pod can be held indefinitely.** The causes are a partitioned node, a replaced claim, or a kubelet that never reports. That is the fence working. The release is a human act: remove the finalizer by hand. The manager records no evidence for it, and `WriterFence` keeps its last `Unknown` reason.
- **Deleting an `Agent` is no longer instant.** It completes only once its Pod is released, and a held Pod keeps it deleting.
- **The manager now deletes something.** It deletes the StatefulSet of a deleting `Agent`. Before this change no Go file under `internal/` outside the tests called a client's `Delete`.
- **The manager now reads Pods, claims and nodes**, with RBAC `get/list/watch/patch` on pods, `get` on claims and `get` on nodes. Pods are watched and cached. Claims and nodes are read uncached, when a Pod is deleted.
- **Turning the fence on rolls every agent's Pod once**, because the Pod template gains the finalizer.
- **Deleting a Pod's token Secret by hand** makes the manager mint a new one for the next Pod. The running Pod keeps the copy it took at start.
- **`terminationGracePeriodSeconds` is not set in this slice.** Sherlock's drain commits the current turn before it releases the writer lock, and the kubelet's SIGKILL at the end of the grace period cuts a longer drain short. That value is to be stated, not assumed, and it is recorded as an open question in `agent.md`.
- **The force-delete e2e spec is written and has not run.** #224 keeps `BeforeSuite` from passing on the host it was written on.

Ruled out:

- **Releasing on Pod `NotFound` or after a timeout.** Neither is evidence.
- **Treating empty statuses or waiting states on a scheduled Pod as "never started".** Both can be a stale report.
- **Keeping the claim baseline in `status`.** That is a read of status as a reconcile input.
- **Releasing an orphaned Pod without persisting its evidence, or leaving every deleted `Agent`'s Pod stranded**, as the alternatives to the `Agent` finalizer.
- **A projected Secret for the token.** A projected file is root-owned and group-readable, which Garam's key-file rule refuses.
- **Adding a `GARAM_ADAPTER_*` variable for the token now.** Its name is garam#1169's to publish.
