# ADR 0077: Build agents only from the control service's feed, and remove the legacy path and the cut-over

> Status: accepted
> Date: 2026-10-20

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #388. garam `72e936973db5f95757ddfb7b3a6bec2a306c95e5`, tag `v0.5.0`, carries `garamsh/garam#1611` and `#1612` (garam ADR-0118). Its `api/machine.yaml` no longer has:

- **Legacy execution.** `GET /definitions`, `POST /definitions/{agent}/claim`, the legacy `POST /agents`, and the staged cut-over with its `agent-cutover` contracts. A delegation can no longer name `agent:cutover` (`api/openapi.yaml` there).
- **The certificate path.** `POST /enrollment`, client-certificate authentication, garam's certificate authorities, and every certificate it issued or renewed, `issuerPem` among them. The operator enrollment token went earlier in the same range (`garamsh/garam#1598`), so an operator enters by the key `issueOperatorKey` issues.
- **What stays.** `serverRootPem` stays in `mintAgentToken`'s answer, read from `GARAM_MACHINE_SERVER_ROOT_FILE`, and the machine listener's serving chain is unchanged.

The owner directed on 2026-10-19, through the lead PM, that all legacy is removed now with no migration of a legacy agent (recorded on #388).

This repository held four things for that path:

- **The manager's legacy path.** A poller that read and claimed garam's definitions (ADR 0007), a constructor that built an `Agent` from each (ADR 0009, ADR 0018), and a reporter that sent garam the provisioning state of the agents it built (ADR 0016). Each `Agent` was marked with the source it came from, `spec.identity.source` `Garam` or `Control` (ADR 0043). A `Garam`-source agent's adapter ran unfenced, with the outbox alone (ADR 0060).
- **The cut-over.** Four console stages, an import table, and the feed's `origin: cutover`, which moved a GRN from `Garam` to `Control` (ADR 0050).
- **The certificate era.** The control e2e suite enrolled both of its operators at `POST /enrollment` and kept the controller's certificate to make legacy agents for the cut-over tests.
- **Residue.** Flags, metrics labels and annotations that only these paths read.

[ADR 0068](0068-keep-no-backward-compatibility-path-during-development.md) keeps no backward-compatibility path during development. [ADR 0055](0055-change-the-control-services-schema-only-by-forward-only-versioned-migrations.md) changes the control service's schema only by a new forward-only migration.

aurora runs operator `v0.2.0` with `--garam-address` set and no control service. Its one legacy agent is being deleted on garam's side.

## Decision

1. **The manager builds agents only from the control service's feed.** The poller, the constructor and the claim are removed, with `ListDefinitions` and `ClaimDefinition`. So is the reporter with `ReportProvisioningState`, which reported only agents not on the `Control` source. A manager with no `--control-address` builds no agents and calls garam for nothing, whatever `--garam-address` says.
2. **An `Agent` carrying `spec.identity` is the control service's.** `spec.identity.source` is removed, and with it its CEL rule. `spec.revision` and `spec.stopped` are refused on an `Agent` with no identity. `status.agent` and `status.epoch` are removed, since only the constructor wrote them and only the reporter read them. The renderer refuses an existing `Agent` under the GRN's name that carries no identity or another GRN, which is one a person wrote.
3. **Every placed adapter activates through the control service.** The adapter is built only where `--control-address` is set too. The unfenced adapter goes, and so does its outbox-only shape. Every agent's credential holds both tokens. `agent.garam.sh/tokens-fenced` is removed, and the refresher runs only where `--control-address` is set, minting every control token for `--control-grn`.
4. **The flags only those paths read are removed.** These are `--garam-poll-interval`, `--garam-report-interval`, `--agent-storage-size` and `--agent-workspace-storage-size`. The renderer sizes both claims from the profile. `--agent-image`, `--garam-address`, `--garam-trust-file` and the three key files are required where `--control-address` is set.
5. **The control service serves no cut-over.** The four console routes, `agent:cutover`, the `agent-cutover.v2` contract, the definition domain's imports and the feed's `origin` are removed. Migration 3 drops `cutover_imports`, and migrations 1 and 2 are not edited. A revision an unswitched import left with no assignment stays in `definitions` and is released to no controller.
6. **No operator holds a certificate.** The control e2e suite registers both operators and has garam issue each a key, and enrolls neither. Its delegation names no `agent:cutover`. `internal/certificate` stays, because it is the control API's serving-certificate reloader, not client authentication. The mint answer's `serverRootPem` stays required (`internal/garam/client.go`), because it is still answered and is what the adapter and the agent verify garam's listener against.
7. **No legacy agent is migrated.** An `Agent` that a legacy construction built is deleted before this version is deployed. It carries `spec.identity`, so by decision 2 this version reads it as the control service's: the refresher mints it a token for the control service, and its adapter is built fenced, to activate through a control service that holds no definition for its GRN and never released it.

## Consequences

- **Superseded.** [ADR 0050](0050-cut-a-legacy-agent-over-through-console-stages-garam-records-and-release-it-only-on-the-switch.md) is superseded whole. So is [ADR 0016](0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md). These are superseded in part:
  - [ADR 0007](0007-claim-definitions-from-a-poller.md): the poller and its claim. Its division between what a definition says and what this operator builds stands.
  - [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md): the construction of a claimed agent. The image stays this operator's configuration.
  - [ADR 0018](0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md): the constructor's correction. The renderer writes `spec.image` on every render, and a restart renders every agent again.
  - [ADR 0043](0043-pull-managed-agents-desired-state-from-the-control-service-and-keep-each-grn-on-one-source.md): the source marker.
  - [ADR 0060](0060-give-every-placed-adapter-its-agents-outbox-on-either-source.md): the unfenced adapter's outbox.
- **aurora.** A manager configured as aurora's is today, with `--garam-address` and no `--control-address`, builds no agents after this change. aurora deletes its legacy `Agent` before deploying this version (decision 7), and drops from its overlay any flag decision 4 removes.
- **Deployer surface.** Go's flag parsing refuses a flag it does not define, so a manager still passed a removed flag does not start. The CRD loses three fields. RBAC is unchanged: the renderer still creates `Agent`s, and the token store and the recoverer still create Secrets.
- **Tests.** The cut-over e2e tests and their fixtures go. The Kind suite's legacy move test becomes a revision roll on an `Agent` with an identity, which keeps the roll's coverage (#340).

## Rejected alternatives

- **Keep `spec.identity.source` with `Control` as its only value, for a person's `Agent` carrying an identity.** Nothing produces an unfenced identity once garam serves no definition, so the field would mark a path nobody can reach (ADR 0068). An `Agent` with no identity, a person's agent with no adapter, is supported as before.
- **Keep the reporter for managed agents.** It never reported them, and whether a managed agent's provisioning state reaches garam is a decision of its own, not part of this removal.
- **Keep `cutover_imports` with no route writing it.** A table nothing reads is a second account of state nobody can act on. Dropping it is a new migration, as ADR 0055 requires.
- **Fail the manager where `--garam-address` is set without `--control-address`.** Building nothing is the stated outcome for that configuration, and refusing it adds a check for a case that harms nothing.
