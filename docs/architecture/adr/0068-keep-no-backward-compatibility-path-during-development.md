# ADR 0068: Keep no backward-compatibility path during development

> Status: accepted; the control service's upgrade path it kept for #329 removed by ADR-0069
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The owner directed on 2026-10-09, in the PM session, that during development this repository keeps only the correct structure, with no path kept for an earlier version of itself or of the images it runs: "옛 버전까지 고려할 필요 없어. 옳은 구조만 남기는게 개발 버전에서 목표야" (#327). The owner then directed that the lab be replaced wholesale once this lands, rather than migrated step by step (#327, 2026-10-09).

Four manager flags exist only so that a deployment could keep running an older agent image, an older adapter image, or an agent created by an older operator. Each is off by default:

| Flag | Decided by | What it waited for |
|---|---|---|
| `--agent-assignment-epoch` | [ADR 0037](0037-carry-an-agents-identity-in-its-spec-and-start-the-agent-under-it.md) | an agent image accepting `--assignment-epoch` |
| `--agent-instructions-file` | [ADR 0045](0045-give-garams-reply-instruction-as-an-operator-instructions-file.md) | an agent image of `sherlock` `v0.1.0` or later; off kept [ADR 0041](0041-join-garams-reply-instruction-to-the-ego-wherever-the-adapter-is-placed.md)'s joined ego |
| `--agent-migrate-shared-claims` | [ADR 0047](0047-replace-a-shared-claim-statefulset-only-where-the-migration-is-turned-on.md) | a person suspending each shared-shape agent and copying its state |
| `--agent-adapter-control` | [ADR 0049](0049-give-a-managed-agents-adapter-the-control-services-settings-behind-a-switch.md) | a control service serving activation, and an adapter image of `garam@e81a1e0` or later |

ADR 0049 also gave every unfenced adapter `GARAM_ADAPTER_GATEWAY_AGENT`, which only `garam@fdfb76d`'s adapter requires. `garam@e81a1e0` reads it nowhere, and neither does `garam@d6874d2` (`internal/cli/delivery.go`, `internal/cli/cli.go`).

The lab's `state-agent-75a5b4c54b12dca4-0` holds eleven days of an agent's memory, and the cluster has no backup (#327).

ADR 0047 also added the `StateIsolated` condition, and the `Isolated` column of `kubectl get agents`, so that a person could list the agents the gated migration had not yet reached.

## Decision

**This repository keeps no branch whose only purpose is an earlier version of itself or of an image it runs.** What each removed switch did when on is what this operator does, with no flag:

- **The assignment epoch.** An agent whose spec carries one is passed `--assignment-epoch <epoch>`. This supersedes ADR 0037's "only where the manager's `--agent-assignment-epoch` flag is on".
- **The reply instruction.** Wherever the adapter is placed, it is the agent's operator instructions file, passed as `--instructions-file`, and the ego file is `spec.ego` alone. The joined ego is gone. This supersedes ADR 0045's switch and its off state.
- **The shared-claim replacement.** A StatefulSet whose workspace shares the state claim is replaced on the first reconcile that meets it, by [ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md)'s orphan-delete replacement. This supersedes ADR 0047 entirely. The owner decided what the replacement keeps (#327, 2026-10-09):
  - **The existing state claim is kept.** The new StatefulSet mounts the same claim by its existing name, `state-<agent>-0`, so the memory on it survives.
  - **Only the workspace gets a new claim, and it is empty.** The old workspace contents are not copied. They stay on the state claim under `workspace/`, where no workspace reads them.
  - **ADR 0044's seed is removed with it:** the `workspace-seed` init container, the `agent.garam.sh/workspace-seed` annotation, and the decision to seed. This supersedes that part of ADR 0044.
- **`StateIsolated`.** The condition, its `SeparateClaims` and `SharedClaim` reasons, and the `Isolated` column are removed. A reconciled workload is always in the separate shape, and `Synced` already reports `WorkloadReplacing` while a replacement runs.
- **The adapter's control settings.** Every `Control`-source agent's adapter gets them, and activates through the control service. A `Garam`-source agent's adapter stays unfenced: that is garam's legacy mode for an agent that registers no placement, not a compatibility path. This supersedes ADR 0049's switch.
- **`GARAM_ADAPTER_GATEWAY_AGENT`.** It is given to no adapter. This supersedes the part of ADR 0049 that kept it for every unfenced adapter.

**The minimum images follow.** A deployment of this revision runs an agent image that accepts every argument this manager passes, and an adapter image of `garam@e81a1e0` or later. The agent image's minimum is `sherlock` `v0.2.0` (`b3c05c24`), read from sherlock's flag definitions:

| Argument | Passed | `v0.1.0` (`44aaa555`) | `v0.2.0` (`b3c05c24`) |
|---|---|---|---|
| `--agent-id` | always | `cmd/sherlock/agent.go:80` | `cmd/sherlock/agent.go:81` |
| `--assignment-epoch` | where the spec carries an epoch | `cmd/sherlock/agent.go:81` | `cmd/sherlock/agent.go:82` |
| `--ego-file` | where the spec declares an ego | `internal/config/config.go:176` | `internal/config/config.go:182` |
| `--instructions-file` | wherever the adapter is placed | `internal/config/config.go:178` | `internal/config/config.go:184` |
| `--require-store` | on the start after a memory move ([ADR 0065](0065-move-an-agents-memory-only-through-a-verified-copy-it-accepts-before-it-serves.md)) | not defined | `internal/config/config.go:186,191` |

`v0.1.0` accepts every argument the removed switches gated. It refuses `--require-store`, which this manager already passed before this decision, so an agent on it cannot start after a move.

**What stays, with its reason:**

- **A move of an agent whose workspace still shares its state claim is refused `SharedClaim`** ([ADR 0065](0065-move-an-agents-memory-only-through-a-verified-copy-it-accepts-before-it-serves.md)). A move copies the whole state claim, and a shared claim holds the files the agent's code wrote as well as its memory. The replacement removes the shape on the agent's first reconcile, so the refusal meets only a move asked of an agent before that.
- **The control service's schema migrations from the published `7c216469476d`, ADR 0058 and ADR 0063's archived-creation handling, and the CRD and schema upgrade tests** stay for now. Under this directive they go too, in their own pull request (#329): a schema squash and a rewrite of the upgrade path deserve their own review.

## Consequences

- **The manager's flags shrink by four.** A deployment that sets any of them fails to start, because the flag parser refuses an unknown flag.
- **A shared-shape agent is replaced on the first reconcile** of a manager carrying this decision. Its Pod rolls once, through the writer fence, its memory stays on its `state` claim, and its workspace starts empty.
- **A deployment on an older agent image fails to start its agents.** `--assignment-epoch` or `--instructions-file` is refused by an image older than `sherlock` `v0.1.0`, and `--require-store` by one older than `v0.2.0`.
- **A deployment on an older adapter image fails to start its `Control`-source agents' adapters,** and a `garam@fdfb76d` adapter fails without `GATEWAY_AGENT`.
- **A stored `StateIsolated` condition** on an existing `Agent` is not removed by this operator: it stops being written, and nothing reads it. The lab is replaced wholesale (#327), which leaves none.
- [ADR 0060](0060-give-every-placed-adapter-its-agents-outbox-on-either-source.md)'s "the control settings stay where ADR 0049 put them" now reads as every `Control`-source agent, with no flag.

## Rejected alternatives

- **Turn each switch on by default and keep the flag.** It keeps the off branch, and its tests, for a deployment that the directive says not to keep.
- **Keep the gated shared-claim migration for the lab's existing agents.** The owner chose to replace the lab wholesale instead (#327).
- **Keep the seed, so the workspace keeps its files.** The owner chose an empty workspace (#327). The seed existed only for an agent an earlier operator built in the shared shape.

## Errata

### 2026-10-10 — the lab's claim held no memory

Context says the lab's `state-agent-75a5b4c54b12dca4-0` "holds eleven days of an agent's memory". It did not. gitops fingerprinted the store read-only before the migration: every table held 0 rows, and `memory.db` was 57,344 bytes holding the schema only (#340). gitops also measured why (#340). The lab's manager ran without `--agent-adapter-image`, so `adapterBuilt` (`internal/controller/agent_statefulset.go:864-870` at `fe5c70f`) was false and the agent's Pod carried no adapter; the manager's startup log said "Building agents with no adapter: agent-adapter-image or garam-address is unset, so garam delivers them no message" (`cmd/main.go:276` there). No Service exposed the agent's gateway either. No message could ever reach the agent, so an empty store was expected, not a dropped write.

The decision to keep the existing state claim by name stands. It keeps whatever memory a claim holds, and this one held none.

### 2026-10-10 — the replacing StatefulSet did not roll the adopted Pod

Decision restores ADR 0044's replacement, whose last step has the new StatefulSet adopt the orphaned Pod and delete it to roll it. At manager `4c425a7` the adoption happened and the delete did not:
- the new StatefulSet recorded `currentRevision == updateRevision`;
- the Pod kept the old `controller-revision-hash`;
- `Synced` and `Available` both reported success;
- the Pod moved only after a manual suspend and resume (#340, lab, 2026-10-10).

The operator now deletes a Pod whose revision is not the StatefulSet's update revision, through the writer fence and never forced. It reports `Synced` `False` (`WorkloadRolling`) and `Available` `False` (`ReplicaOutdated`) until a Pod on that revision runs. While the StatefulSet's status has not yet observed its generation, it reports `RolloutNotObserved`, and never the workload reconciled. The decision stands.
