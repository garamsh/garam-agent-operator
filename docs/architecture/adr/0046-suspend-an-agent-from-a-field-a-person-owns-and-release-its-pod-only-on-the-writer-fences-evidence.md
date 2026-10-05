# ADR 0046: Suspend an agent from a field a person owns, and release its Pod only on the writer fence's evidence

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #254. No supported way to stop an agent exists:

- The reconciler writes `replicas: 1` on every pass.
- Deleting the `Agent` is not a stop:
  - its credential Secret is owned by the `Agent`, and is garbage-collected with it;
  - a managed agent's first certificate cannot be issued twice (#218);
  - a `Control` `Agent` is recreated by the desired feed at once (ADR 0043).

A consistent copy of an agent's state needs the agent stopped. After [ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md), the `state` claim is mounted into the agent's container alone. `sherlock@b3c05c2:docs/architecture/deployment.md` §Snapshot and restore takes a copy only from a writer that drained and positively stopped, through `deploy/runtime-fixture/snapshot.sh`. The gitops PM needs that copy because no backup exists on that storage class (gitops #14).

Two writers could own the field: a person, or the desired state the renderer and the poller write. The PM settled the bindings on this dispatch, recorded on #254.

## Decision

**`spec.suspended` is a person's.** It is an optional boolean, and absent means false.

- **Neither writer touches it.** The renderer (ADR 0043) copies only the fields it renders into the `Agent` it read, and writes them with a merge patch. The poller's construction and its image correction name no such field.
- **Ownership is visible.** Every write the renderer and the constructor make to an `Agent` carries a named field manager, `garam-operator-renderer` or `garam-operator-constructor`. The person's manager therefore stays the owner of `f:spec.f:suspended` in the `Agent`'s `managedFields`.
- **Not server-side apply.** Those writers are not moved to apply patches, because that would change conflict handling for every field they write.

**Suspending asks the StatefulSet for no replica, and does nothing else.**

- The reconciler writes `replicas: 0` where it would write 1. The StatefulSet is not replaced, and the manager deletes no Pod.
- The StatefulSet controller deletes `agent-0`. [ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md)'s fence holds it until its writers are seen to stop, records `status.writerStopped`, mints the next placement token where the adapter is placed, and only then releases it.
- An unverified fence keeps the Pod, as on every other path.
- Nothing is deleted: not the `Agent`, its credential Secret, its credential-request and placement Secrets, or either claim.

**Resuming asks the same StatefulSet for its replica again.** Its claim templates are unchanged, so the next `agent-0` binds the claims the last one had, under the same UIDs, with the same credential.

**ADR 0044's replacement is not ordered here.** This decision covers suspending and resuming a StatefulSet of the shape the manager builds. #256 gates that replacement behind a manager flag, and decides how a migration's copy is ordered around it.

**Status reports it.** The new condition is `Suspended`:

- `True`, reason `Suspended`, once the spec suspends the agent and its Pod is gone. Nothing then mounts its claims, and this is a copy's signal.
- `False`, reason `Suspending`, while the spec suspends it and the Pod still exists, running or held by its fence. `WriterFence` says which.
- `False`, reason `NotSuspended`, otherwise.

While suspended, `Available` is `False` with reason `Suspended`. The garam provisioning report, which reads readiness, then says `provisioned`, as for any workload with no ready replica.

## Consequences

- **A person can stop and start an agent without losing anything.** Its identity, credential, claims and placement history are kept.
- **A suspend can wait indefinitely.** A Pod whose fence stays unverified keeps `Suspended` at `False` until its writers are seen to stop or a person releases it. The copy waits with it.
- **A copy is taken only after `Suspended` is `True` and `WriterFence` shows the evidence recorded** (`agent.md` §Copying an agent's state).
- **The renderer's and the constructor's writes appear under their own names** in `managedFields`. Before this change they appeared under the manager's default name.
- **envtest runs neither the StatefulSet controller nor a kubelet.** So the integration tests delete the Pod and report its containers as those two would. That the next Pod binds the same claims is shown by the claim UID it records.

Ruled out:

- **Rendering `suspended` from desired state.** A stop for a copy is an operator's act on one copy of an agent, not part of its definition. A rendered field would be cleared by the next revision, or would need the control service to carry an operator's maintenance state.
- **Deleting the Pod from the manager on suspend.** The StatefulSet controller deletes it on the scale-down, and the fence is the only release.
- **Moving the renderer and the constructor to server-side apply.** See above: that would change conflict handling for every field they write.
