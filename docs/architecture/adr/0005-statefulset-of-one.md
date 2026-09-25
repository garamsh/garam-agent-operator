# ADR 0005: Run an agent as a StatefulSet of one replica

> Status: accepted
> Date: 2026-08-17

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

An `Agent` describes one agent, and the operator has to choose the workload that runs it. Three shapes were considered: a Deployment, a bare Pod with a PersistentVolumeClaim beside it, and a StatefulSet.

The fact that decides it is not visible from this repository. The agent keeps its state in a single-writer store on a persistent volume, so two agent processes against one volume is not a degraded mode — it is corruption. The replica count is therefore pinned at 1 by the storage engine, not by preference.

A **Deployment** fails on that directly. Its default rolling update creates the replacement Pod before terminating the old one, so an update runs two agents against one volume for the overlap. `Recreate` avoids the overlap but gives up the ordering guarantee on every other axis and still leaves the volume attached through a plain PVC reference rather than a claim bound to the workload.

A **bare Pod plus PVC** has no overlap, because there is only ever one Pod. It is the simplest thing that works, and it was the closest alternative. What it does not have is rescheduling: a Pod does not survive its node. Recovering from a node failure becomes something a human or a later controller has to do, and building that is building the part of a StatefulSet that matters here.

A **StatefulSet** with `replicas: 1` gives ordered replacement — the old Pod is fully terminated before the new one starts — and `volumeClaimTemplates` binds the volume to the workload rather than to a claim the operator manages separately.

The external side does not help. `garam`'s `epoch` fences a *replaced operator*, not a restart by the same one: every `Assign` call raises it, but the only production caller is agent creation and no reassignment operation exists on the machine surface, so a restart never moves it. Two Pods of the same assignment therefore both hold valid credentials and are indistinguishable to `garam`. Message claims are exclusive (`FOR UPDATE SKIP LOCKED`), which guarantees one reader at a time, not one process in existence. Exclusion is entirely the workload's job, which is what makes the ordering guarantee load-bearing rather than a convenience.

## Decision

The workload an `Agent` produces is a StatefulSet with `replicas: 1` and a `volumeClaimTemplates` entry for the agent's state. `spec.storageSize` and `spec.storageClassName` configure that template.

## Consequences

Easier: the ordering guarantee is the only thing standing between the design and two writers, and it is a property of the workload rather than something the controller has to enforce. Node failure reschedules without anyone intervening.

Harder: a StatefulSet is more machinery than one agent needs, and `volumeClaimTemplates` is deliberately hard to change — a claim template is immutable after creation, so altering `spec.storageSize` on an existing `Agent` cannot be satisfied by editing the StatefulSet. That is a real limitation this decision accepts and does not solve; whatever answers it will be its own decision.

Ruled out: a Deployment, at any update strategy. A bare Pod plus PVC, on rescheduling alone — it is the shape to revisit if the StatefulSet's rigidity costs more than the rescheduling is worth.
