# ADR 0056: Publish a deployment's profile versions through a subcommand of the control binary, and refuse settings no workload could run with

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #294. A profile is a named set of execution settings, published in numbered versions that are never changed (`control.md`).

**Who publishes one.** garam keeps profile publishing with the deployment: "Publishing one stays with the deployment" (`garamsh/garam@59fe68d` `internal/operator/delegation.go:62`). So no console route under operation authority publishes one.

**What a deployment had.** Nothing this repository documented. In `garamsh/garam#1196` run 2 every profile was inserted by SQL, and so was every profile in this repository's e2e.

**What SQL skipped.** `definition.PublishProfile` checked nothing either. The e2e's profiles carried `'{}'`, with no storage size, and such a profile reaches the feed.

The PM settled the binding choices on this dispatch: the form, the checks, the version rule, and that the command does not migrate the schema.

## Decision

**A subcommand of the control binary: `control publish-profile --file <profile.yaml> [--file ...]`.**
- **What it writes through.** `definition.Service.PublishProfileVersion`, the definition domain's own path, into the store `CONTROL_DATABASE_URL` names, as the server's does. It serves nothing.
- **The file.** One profile version, in the read route's names: `{organization, name, version, resources, storageSize, storageClassName?, workspaceStorageSize?}`. A field the format does not name is refused.
- **Its outcome.** Files are published in order, and the command stops at the first refused. Each file's outcome, `published`, `unchanged` or `refused`, is printed on stdout with its organization, name and version, and the reason for a refusal on stderr.
- **Exit codes.** `0` when every file was published or unchanged. `1` for a refusal or an unreachable store. `2` for a command line or a file that is not one.

**How a deployment runs it.** It runs as a Job in its gitops overlay, from the same image the Deployment runs, with the same database Secret. The profile files come from a ConfigMap. The command is idempotent, so a re-sync running it again changes nothing.

**It does not migrate the schema.** Migrating is the server's alone, at its start. A Job that migrated could race a rollout, and could advance a database an older server is still serving.

**Every profile publication is checked**, by `ExecutionSettings.check`, on this path and on the in-process `PublishProfile` alike. It refuses with `ErrInvalidProfile`:
- a storage size not above zero;
- a workspace size not above zero, where one is named — the manager's own rule for it (ADR 0053);
- a storage class name that is not a DNS-1123 subdomain, where one is named;
- a resource request above its own limit.

A profile carries no configuration, so `Configuration.check`, which template publication runs, does not apply to it. Templates keep their own check through their console route.

**A published version is never changed.** The file names the version it publishes:
- **The same settings again.** They are a no-op, answered `unchanged`. "The same" is `ExecutionSettings.Same`: semantic equality, so `1024Mi` is `1Gi` and no resource list is an empty one.
- **Other settings under a published version.** Refused with `ErrProfileVersionConflict`. The stored version keeps its own.
- **A version neither published nor the next.** Refused with `ErrProfileVersionGap`, so versions stay numbered from 1 with no gap.
- **One step.** The read and the insert run in one transaction under the profiles lock. Two concurrent publishers of one version store one row: the other is answered unchanged or refused.

## Consequences

- **A deployment publishes profiles without SQL**, and a profile no workload could run with never reaches the feed through any path this repository offers.
- **A bad profile fails one Job, visibly, and the service keeps serving.**
- **Profiles inserted by SQL before this decision are not re-checked.** A profile with no storage size stays readable and nameable. Only publication checks.
- **The e2e publishes its profiles through the built binary.** Only the schema test that stores a second row under one key inserts by SQL, because that row is one the command answers as unchanged.

Ruled out:
- **A manifest the server loads at start.** A profile that is invalid, or changed under a published version, would either stop the server from starting or have to be skipped silently. A changed ConfigMap would also reach the store only on a restart.
- **A console route.** garam keeps profile publishing out of operation authority (`delegation.go:62`).
- **Numbering the version in the command, as `PublishProfile` does.** Re-running the same file would then publish a new version each time, so the command could not be idempotent.
- **Reading, then publishing the next version, outside one transaction.** Two concurrent publishers could number two versions for one intent.
