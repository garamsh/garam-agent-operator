# ADR 0065: Move an agent's memory only through a verified copy it accepts before it serves

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #296 asks for the managed counterpart of garam's standalone move. Until this ADR, a managed agent's memory moved only by hand: `agent.md` §Copying an agent's state and [ADR 0047](0047-replace-a-shared-claim-statefulset-only-where-the-migration-is-turned-on.md)'s lab order.

**garam's standalone move.** ADR-0089 (`garamsh/garam@7d67c88`) sets the order:
1. drain the running generation;
2. observe that its writer stopped, which needs the last runtime to have exited 0;
3. copy, with a helper that holds the store's writer lock;
4. verify, by a manifest of each entry's path, mode, owner and SHA-256, the source read before and after;
5. start the next generation on the copy under sherlock's `--require-store`;
6. activate it, replacing the old activation.

A refused move keeps the old volume in use, unwritten, with the agent stopped on it. The refused volume is kept and never used again. Managed moves are out of its scope: "the Operator's and Sherlock's" (ADR-0089:107-108). garam's ADR-0078 requires the same four things of any move: quiescence, a consistent copy of the database and the outbox, a fenced old writer, and a verified restore before activation.

**What sherlock v0.2.0 states** (`sherlock@b3c05c2`):
- **The stored state** is `memory.db`, `-wal`, `-shm` and `outbox/`. The writer lock is not state.
- **Positively stopped** means the process exited 0 after its drain, and its lock is free.
- **A restore** goes to a fresh path.
- **Not starting a copy while the old writer might run** is the lifecycle authority's job (`docs/architecture/deployment.md:264-270`).
- **`--require-store`** refuses an absent or uninitialized store instead of creating one (`internal/config/config.go:85-90,186-191`, `internal/memory/sqlite/conversation.go:302-313`). It is the only acceptance v0.2.0 has.

**What garam asks of a managed move: nothing beyond activation.**
- Its activation route assumes the caller "has proven the placement and that the previous writer stopped before it calls" (`api/machine.yaml:1107-1109`). `replacesActivationId` must name the latest activation (`:1121-1124`).
- Its request carries no storage, restore or acceptance field (`:2481-2507`).
- Control already sends the agent's latest activation as `replacesActivationId` (`control.md`).
- **garam's PM confirmed it** on #296 (issuecomment-6029079296): garam owes nothing new for the managed move and wants no audit field. Where memory lives belongs to sherlock and whoever places it, so a "moved copy" flag would be storage provenance garam can neither verify nor act on. A move's durable record lives with its verified manifest, which is the target claim's `agent.garam.sh/move-digest`.
- **The invariant garam's PM named:** activate only after sherlock accepted the copy (`--require-store`, serving), with `replacesActivationId` the latest activation, so garam fences the old generation the moment the new one is current.

**No image this operator deploys has `sqlite3`.** Measured on the copy image (`busybox:stable-musl`) and on `sherlock-workspace:v0.2.0`. SQLite's `integrity_check`, which sherlock's own `snapshot.sh verify` runs, cannot run here, and garam's move does not run it either.

## Decision

**A person declares a move with `spec.memoryMove: {id, storageClassName, storageSize}`.**
- Neither the renderer nor the constructor writes it, for [ADR 0046](0046-suspend-an-agent-from-a-field-a-person-owns-and-release-its-pod-only-on-the-writer-fences-evidence.md)'s reason: a move stops the agent and copies one agent's state. The profile's storage stays what a new agent's claim is made with.
- The move's target is the claim `<agent>-state-<id>`. Admission refuses a changed target under the same id.
- A move works on every source.

**The manager runs every step, in this order:**
1. **Stop.** While a move is unsettled, the workload asks for no replica, which is `spec.suspended`'s mechanism. [ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md)'s fence releases the Pod only on evidence.
   - The fence now also records a state claim's last writer on the claim, as `agent.garam.sh/last-writer`: running when it first sees a Pod, then stopped with the agent's exit before the finalizer comes off.
   - A move begins only on a stopped record with exit 0 and no Pod left. A writer SIGKILLed, released by hand, or never created ([ADR 0061](0061-release-a-pod-whose-writers-were-never-created-only-on-the-kubelets-terminal-phase.md)) is refused `WriterNotDrained`, and nothing of the move is created.
2. **Target.** The manager creates the target claim with no owner, since the memory outlives the Agent. The move's record is annotated on it: the id, the source claim's name and UID, the source's last-writer record, and the phase.
3. **Copy.** A Job runs the copy image as the agent's user, with no service-account token, once (`backoffLimit: 0`), with the source mounted read-only and the target writable.
   - It refuses a held writer lock (`flock -n` on `memory.db.lock`), a target that is not empty, and any entry that is not a regular file or directory.
   - It copies `memory.db`, `-wal`, `-shm` where present, and `outbox/`, with modes, then syncs.
   - It reports the source's digest, taken before the copy, in its termination message.
4. **Verify.** A second Job mounts both claims read-only and reports three things: the source's digest now, the copy's digest, and how many entries the target holds beyond the copy.
   - A digest is the SHA-256 of a sorted manifest. Each entry of the store and the outbox gets one line: its path, mode, `uid:gid` and content SHA-256, or `absent`.
   - The manager compares the values itself. The source's digests before and after must be equal, the copy's must equal them, and the target must hold nothing else. That the Job exited 0 is not the verification.
5. **Switch.** The source claim is annotated `agent.garam.sh/moved-to: <target>`. The claim the memory is on is the end of that chain, followed from the StatefulSet's own claim, so it survives the Agent's deletion and a reconstruct for the same GRN, as the claims do. The StatefulSet then mounts it by name, and the agent is given `--require-store`.
   - The first move replaces a StatefulSet whose template still makes the state claim, with [ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md)'s orphaning delete, with no Pod alive.
   - A later move edits the claim the Pod template names.
   - The source must still be the claim the move began on, with no writer since. Otherwise the move is refused `SourceChanged`.
6. **Accept.** sherlock opens the copy or refuses to start. The adapter activates only a serving runtime (garam ADR-0089:76-84), through control's existing route.
   - The move is recorded accepted once the first Pod on the copy is Ready, as a report: that activation follows acceptance is structural.
   - An agent container that exits non-zero there first refuses the move `AgentRefusedStore`. That includes an image before v0.2.0, which does not accept the flag. The memory goes back on the source.

**A refused move leaves the agent stopped on the source**, which no step writes or deletes. A refusal after the switch takes the source's `moved-to` off again.
- The refused target is kept, phase `refused`, and its id is never used again.
- The agent stays at no replica until a person clears the move or names a new id. Nothing falls back to running on its own.
- A move refused before its target exists leaves nothing at all.

**Every decision is read off the cluster's objects, never off the Agent's status.**
- The sources: the claims' annotations, the Jobs by their hashed names, and the Jobs' Pods' termination messages.
- A manager stopped between two steps resumes at the step recorded. A Job found gone after the target was marked for it refuses the move, because what it did is unknown.
- While any move Job runs, the agent stays stopped, even if the move is cleared.
- `MemoryMove` on the Agent's status is only a report.

**The manager's RBAC grows by what the steps use.**

| Verb | Step |
|---|---|
| persistentvolumeclaims `create` | the target claim (step 2) |
| persistentvolumeclaims `patch` | the last-writer record (step 1), every phase of the move's record (steps 2-6), and the source's `moved-to` (steps 5-6) |
| jobs `create` | the copy and verification Jobs (steps 3-4) |
| jobs `get`, `list`, `watch` | reading their outcome, and waking the Agent when one ends (steps 3-4) |

**`--agent-copy-image` runs the move's Jobs.** It is the deployment's, by digest. A reconciler with none refuses a move `CopyImageUnset`, and the manager already refuses to start without it.

## Consequences

- **A managed agent's memory moves with the agent stopped from its drain until it accepts the copy.** The time it is down is the copy's.
- **The source claim is kept, and stays allocated.** Deleting it after `MemoryMove=Accepted` is a person's step. Its name and UID are on the target's annotations.
- **An agent that did not drain cleanly cannot move until it does.** Clear the move, let the agent run and stop cleanly once, then name a new id. This is garam ADR-0089's consequence too.
- **A move needs agent v0.2.0 or later.** An earlier image refuses `--require-store`, and the move is refused `AgentRefusedStore`.
- **Integrity is the drained source's, carried byte for byte.** No SQLite integrity check runs, because no image here can run one.
- **One window is left.** A Pod started and released by hand entirely while the manager was down leaves its claim's record naming the writer before it. This is the release by hand ADR 0042 already leaves to a person.

Ruled out:
- **The profile's storage as the declaration.** A configure would move every agent on the profile.
- **An init container that copies in the new Pod**, as the workspace seed does. The copy would be verified by the Pod it starts.
- **Waiting for control's activation before recording acceptance.** The feed would need a new field, which is a wire change, for a report.
- **Deleting the source after acceptance.** Nothing here can tell that no one still wants it.
- **Naming the claim in use on the Agent.** An annotation on the Agent is lost with it, and a reconstruct for the same GRN would then start on the claim the memory moved off.
