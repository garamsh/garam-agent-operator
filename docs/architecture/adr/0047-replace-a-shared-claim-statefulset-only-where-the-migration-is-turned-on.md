# ADR 0047: Replace a shared-claim StatefulSet only where the migration is turned on, and report which agents still share

> Status: superseded by ADR-0068
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #256. [ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md) (#251) replaces every StatefulSet whose workspace shares the state claim as soon as the new manager reconciles it:

1. an orphaning delete;
2. a new StatefulSet with two claim templates;
3. a one-time seed of the workspace claim.

That sequence has never run for real. envtest simulates the garbage collector and the StatefulSet controller, and Kind cannot run here (#224).

The lab deploys this manager as one Argo Application with `prune` and `selfHeal` and no sync waves (gitops PM, measured 2026-10-05). It cannot pause between applying the CRD and rolling the manager. So stopping an agent (#254, `spec.suspended`) cannot be ordered before the new manager first reconciles it, unless a safety property is turned off for the duration.

The pattern already exists here. `--agent-assignment-epoch` ([ADR 0037](0037-carry-an-agents-identity-in-its-spec-and-start-the-agent-under-it.md)) and `--agent-instructions-file` ([ADR 0045](0045-give-garams-reply-instruction-as-an-operator-instructions-file.md)) are off by default until the deployment is ready for them.

## Decision

**A manager flag, `--agent-migrate-shared-claims`, off by default, gates ADR 0044's replacement.** This supersedes in part ADR 0044's decision that every shared-shape StatefulSet is replaced, which now holds only with the flag on.

**With the flag off, a shared-shape StatefulSet is reconciled in its existing shape:**

- no orphaning delete, no new claim and no seed;
- the workspace keeps its mount on the state claim;
- no claim template changes. Claim templates are immutable, and the update would be refused with a template diff, or with a mount naming a claim the StatefulSet lacks.

Every other field is reconciled as before, so a spec change still reaches the agent.

**A StatefulSet created where none exists always gets the separate shape, whether the flag is on or off.**

- **A new Agent** gets the separate shape.
- **A StatefulSet recreated over an existing `state` claim with no `workspace` claim** gets the separate shape and the seed. An example is an old agent's StatefulSet deleted by hand.
- **Why the flag does not cover that case.** There is no old StatefulSet left to keep. Creating a shared one would add a boundary violation that nothing asks for.
- **Why it is safe.** The seed starts only with the new Pod. That Pod cannot be created until the old one is gone and its fence released (ADR 0042), so the seed never runs beside a writer.

**With the flag on, ADR 0044's replacement runs unchanged.**

**Status reports which shape an agent runs**, as a condition, `StateIsolated`:

| Status | Reason | When |
|---|---|---|
| `True` | `SeparateClaims` | The workload claims the state and the workspace separately. |
| `False` | `SharedClaim` | The workload is in the shared shape and the flag is off. |
| `False` | `WorkloadReplacing` | The replacement is running. |
| `Unknown` | `WorkloadNotObserved` | The workload was not reconciled. |

A printcolumn, `Isolated`, puts the status in `kubectl get agents`, so the agents still sharing a claim are visible.

**With the flag off, the security boundary of #249 is not yet in force for shared-shape agents.** Code such an agent runs can still read and overwrite its state, as gitops #288 measured. The flag is the gate for closing that boundary, and `StateIsolated=False` lists every agent it is still open for.

**The lab order:**

1. Roll the manager, with the flag off. Shared-shape agents keep running as they are.
2. Set `spec.suspended` on each shared-shape agent (#254). The agent is drained, and its Pod is released by the fence on evidence.
3. Copy each agent's state. That is `memory.db`, its `-wal` and `-shm`, and `outbox/`, copied by `sherlock`'s procedure (`agent.md`).
4. Turn the flag on. Each suspended StatefulSet is replaced with no live Pod.
5. Clear `spec.suspended`. The agent starts on the same state claim, with its workspace seeded once.

## Consequences

- **The first real replacement runs where a person chose it,** with the agent stopped and its state copied, not at the moment a manager rolls.
- **Agents left in the shared shape remain exposed until someone acts,** and `StateIsolated=False` says which ones they are. Turning the flag on without suspending first runs ADR 0044's replacement against a live Pod, which the fence still guards (ADR 0042), but without a copy taken first.
- **The flag can stay on once no agent reports `SharedClaim`.** Removing the flag and its off state is a later change, once no deployment carries a shared-shape StatefulSet.
- **The suspended-agent test is owed.** `spec.suspended` is #254's and is not on `dev`, so this change tests a replacement with no live Pod, which is what a suspended agent leaves. A test with the field itself is owed by whichever of #254 and #256 lands second.
