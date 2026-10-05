# ADR 0044: Give an agent's state and its workspace separate claims, and replace an existing StatefulSet without losing either

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #249. [ADR 0023](0023-run-an-agents-workspace-as-a-second-container-this-operator-names.md) put the workspace on the volume [ADR 0005](0005-statefulset-of-one.md) claims for the agent's state, kept apart from the memory store by two path constants and nothing else.

The gitops PM measured the result on garam-dev on 2026-10-05 (gitops #288), in `agent-75a5b4c54b12dca4-0`:

- Both containers mount the same claim at `/var/lib/sherlock`.
- The workspace's exec uid is 65532, which is the Pod's user and the owner of `memory.db` and `memory/`.
- At that uid, appending to `memory.db` and reading it both succeeded. The control, `touch /etc/probe`, was refused.

So the code the agent decides to run, which the design treats as untrusted, can read the agent's whole conversation history and overwrite its store. There is no escape from a container and no escalation across identities. What is missing is any boundary between an agent's state and the code it runs.

`garamsh/sherlock` `docs/architecture/deployment.md` §Volumes and ownership, at dev `44aaa55`, already describes the shape:

> **Two writable volumes, not one shared filesystem.** … nothing the two containers exchange goes through a file … The sample deployment mounts one root into both, which is that file's convenience rather than a requirement.

The same section says the outbox is derived from `memory-path`, so it stays wherever the store is.

The PM decided on separate claims. A mode or uid split on one claim was rejected: it keeps both trees on one filesystem and depends on modes staying right. The PM then settled the bindings on this dispatch.

## Decision

**Every StatefulSet carries two claim templates.** This supersedes ADR 0023's clause that the workspace serves a subtree of the state volume. The rest of ADR 0023 stands.

- **`state` keeps its name.** It holds the memory store and the outbox, and only the agent's container mounts it. Because the name is unchanged, the claim an existing agent holds is the claim it keeps.
- **`workspace` is new.** Only the workspace's container mounts it, at the same `/var/lib/sherlock`. The directory it serves keeps its path, `/var/lib/sherlock/workspace`, and the memory path does not exist in that container.
- **Both templates are built whether or not `--agent-workspace-image` is set.** A claim template cannot change after creation, so toggling the flag never forces another replacement.
- **The adapter's outbox access (garam#1169)** is to mount `state` into the adapter only. It is not built here.

**Sizes.**

- The workspace claim is sized by `spec.workspaceStorageSize`, which is optional and must be greater than zero. Where it is unset, the claim is sized at `spec.storageSize`, so an existing `Agent` upgrades with nothing set.
- An `Agent` this operator constructs from garam takes it from the optional flag `--agent-workspace-storage-size`, which has no default.
- Carrying it in the control service's profile (ADR 0032) is a follow-up issue. Until then the renderer writes nothing to the field.
- Both claims use `spec.storageClassName`.
- `StorageSizeImmutable` also reports a workspace size the claim cannot follow.

**An existing StatefulSet is replaced, not deleted with what it holds.**

1. The reconciler finds a StatefulSet that has no `workspace` claim template.
2. It deletes that StatefulSet with propagation `Orphan`, preconditioned on its UID. Its Pod and its claims stay where they are. While it is deleting, `Synced` is `False` with reason `WorkloadReplacing`.
3. Once it is gone, the next StatefulSet is created.
4. The new StatefulSet adopts the orphaned `agent-0` by its labels, and its rolling update deletes that Pod.

No path lets two writers mount the state claim:

- **The orphaned Pod is the only writer** until its fence releases. ADR 0042's finalizer holds it until its writers are seen to stop.
- **StatefulSet identity forbids a second `agent-0`.** The next Pod has the old one's name, so it cannot be created while the old one exists.
- **The claim is reused by name.** The fence's `pvc-uid` and creation-time checks therefore still match the claim the old Pod started on.
- **A manager stopped between the delete and the create** leaves the old Pod running alone, and the next reconcile creates the StatefulSet. Whether to seed is read off the cluster, not remembered, so that reconcile decides it the same way.

**The workspace is seeded once, by copy, and only on an upgraded StatefulSet.**

- **Which StatefulSets seed.** A StatefulSet created where the `state` claim exists and the `workspace` claim does not is one replacing the shared shape. It is annotated `agent.garam.sh/workspace-seed: state`.
  - A new agent has neither claim.
  - A StatefulSet deleted by hand from the separate shape leaves both.
  - Neither of those seeds.
- **The seed container.** An annotated StatefulSet's Pod carries an init container, `workspace-seed`. It runs the copy image (`--agent-copy-image`) before the agent and the workspace start.
  - It mounts `state` read-only at `/run/garam/seed/state`, and the workspace claim at `/run/garam/seed/workspace`.
  - If `/run/garam/seed/workspace/.seeded` is absent, it runs `cp -a` from `state/workspace/.` into the claim's `workspace/`. The source stays in place.
  - It then syncs, writes the marker, and syncs again. A stop before the marker is durable runs the copy again. A marker never stands for a copy that is not durable.
  - A state claim with no workspace directory is seeded with nothing and marked.
  - The marker sits beside the directory the workspace serves, not in it.
- **The memory store is untouched.** The seed reads `state` read-only and copies nothing from outside `workspace/`.
- **The seed container stays for the StatefulSet's life.** After the marker it is a no-op. It runs only the operator's script, never code the agent runs, and it reads `state` read-only. Taking it out would roll the Pod a second time, behind a second fenced release, for nothing.

## Consequences

- Code the agent runs cannot reach the agent's state: the memory store, its conversation history and the outbox are on a filesystem the workspace container does not mount.
- `agent.md`'s open question about sharing one volume's size closes. Each claim is sized on its own, and a workspace that fills its volume no longer fills the agent's state.
- Every existing agent's Pod is replaced once, on its own fenced schedule. Its state claim is kept, and its workspace files are copied onto a new claim. The old copy stays on the state claim, unreachable from the workspace, until someone removes it. Nothing here removes it.
- An upgraded agent's Pod carries one more init container than a new agent's, and the state claim is mounted read-only into that container.
- envtest runs neither the garbage collector nor the StatefulSet controller, so the integration tests simulate orphaning and adoption. That the memory path does not exist in the workspace container is checked in e2e, where a kubelet mounts the claims.
