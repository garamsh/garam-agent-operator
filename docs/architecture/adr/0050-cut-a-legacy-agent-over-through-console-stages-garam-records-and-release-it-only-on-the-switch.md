# ADR 0050: Cut a legacy agent over through console stages garam records, and release it only on the switch

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #217. [ADR 0032](0032-own-an-agents-desired-definition-in-this-projects-control-service.md) moves every agent's desired definition here, and [ADR 0043](0043-pull-managed-agents-desired-state-from-the-control-service-and-keep-each-grn-on-one-source.md) keeps each GRN on one source by `spec.identity.source`. garam owns the cutover contract, `agent-cutover.v1`, merged at `garamsh/garam@1a5273d` (ADR-0086 there, `api/machine.yaml` §cutover):
- **Two attempt states, recorded in garam.** Under the agent's execution lock, garam records `frozen` and `switched`.
- **One route per stage.** `getAgentCutover`, `freezeAgentCutover`, `switchAgentCutover` and `rollBackAgentCutover` each take a durable `agent:cutover` reference bound by `requestTarget` to that one route.
- **A digest over the source.** The source's digest is the SHA-256 of the RFC 8785 JSON of `{agentGrn, operatorGrn, values}`.
- **Rollback only from frozen.** A rollback is allowed only from `frozen`.

The PM settled this side's bindings on this dispatch, recorded on #217.

## Decision

**The console drives the stages, one console route per stage, each under the console's operation-authority pipeline** ([ADR 0039](0039-serve-the-consoles-mutations-from-a-console-domain-over-the-definition-domains-surface.md)):
- **The routes.** `POST /v1/orgs/{org}/agents/{agent}/cutover/{import,freeze,switch,rollback}`.
- **The authority.** Each route needs an `agent:cutover` authority on the agent, bound to its assignment, whose `requestTarget` is garam's own route for that stage, as garam receives it.
- **Another stage's reference.** An authority bound to another stage's route is 403 here, before garam is asked. garam compares it again.
- **What control sends.** Control carries the binding's durable reference to garam as `Authorization: Garam-Operation <ref>`, under `agent-cutover.v1`.

**The stages, in order:**
1. **Import.** Control reads the source from garam.
   - **Eligibility.** The agent must be legacy and `eligible`.
   - **The digest.** Control recomputes the digest itself, with `gowebpki/jcs` v1.0.2, and requires it to equal garam's. Its result is pinned to garam's own canonical vector.
   - **What is stored.** The import is keyed by GRN, every value is kept verbatim with its disposition, and revision 1 is stored **inactive**: the request's profile, the imported pins, and no assignment, so nothing of it is released.
   - **What it refuses.** An agent defined here already is refused, and so is another import while one is open.
   - **Nothing is added.** No configure is accepted for the agent until the switch.
2. **Freeze.** garam freezes the source against the import's digest, and control records `frozen` on garam's answer.
3. **Switch.** garam records `switched`. Only on its answer does control, in one transaction, record `switched` and make revision 1 active: recorded for garam's assignee and epoch, at a new position, so the feed releases it.
4. **The manager's switch** of `spec.identity.source` from `Garam` to `Control` follows, and is the manager's (below).
5. **Retirement** is read-only, and has no route here.

**Rollback only from frozen.** A rollback of a switched import is refused here, 409 `reverse_migration_required`, before garam is asked. A frozen one is rolled back at garam, and control then discards the import and its revision 1.

**Dispositions.** Every value of the source needs a disposition:
- **`[a-z0-9-]` keys.** A key matching `[a-z0-9-]+` was never applied by the legacy path, which read only `tools.pins.*` (`internal/garam/client.go` `readValues`, at `85cf8c9`). It is archived as `never-applied` and does not block.
- **`tools.pins.*` and other keys.** Any other key needs a disposition in the import request: `import` or `archive`. A `tools.pins.*` key is in that set: the poller applied it only at first construction (ADR 0032's gap), so the current pin may not be what the agent runs, and control cannot see the Agent to tell. The person running the cutover decides, with the running Agent in view.
- **What `import` allows.** Only a `tools.pins.<tool>` key may be imported, into revision 1's pins. `archive` keeps a value verbatim and applies it nowhere.
- **A missing disposition.** A key with none is 422 `key_disposition_required`, which names the keys.

**Revision 1's first activation rests on an explicit configure authorization.**
- **The configure authority.** The switch request also carries an `agent:configure` authority for the agent, in the header `Garam-Configure-Operation: Garam-Operation <authority>`. Control stores its durable reference, and the activation of revision 1 is sent under it.
- **Its own request id.** garam binds a request id to one operation, so that authority is minted under its own request id, which the switch's body names as `configureRequestId`. Both authorities bind the same body's digest.
- **Why not null.** garam's contract would admit a null `operationRef` for a switched legacy agent. It is deliberately not used, because revision 1 carries content chosen at import (the profile and the dispositions) that the legacy runtime never ran.

**The C2 feed gains `origin`.** This is an addition to [ADR 0040](0040-release-desired-state-to-controllers-from-a-distribution-domain-each-decision-proved-by-garam.md)'s wire.
- **What it marks.** An agent whose revisions began with a switched cutover import carries `"origin": "cutover"`, and every other agent carries none.
- **What the manager does.** Seeing it on a GRN whose `Agent` is `Garam`-source, the manager is to flip `spec.identity.source` to `Control` (ADR 0043's one-way rule) and render. Nothing reads Status.
- **The order it keeps.** Nothing is released before the switch, so no GRN is rendered from both sources. The manager's change is a later slice.

**Placement.** No domain is new:
- the stage routes and the cutover port are `internal/console`'s, its implementation in `internal/console/cutover/` over `internal/garammachine`;
- the import and revision 1 are `internal/definition`'s;
- the feed's field is `internal/distribution`'s.

## Consequences

- **A switch ends garam's hold.** Between garam's switch and the manager's flip, no desired writer acts on the agent, which keeps running on what it was built from (garam ADR-0086).
- **A frozen agent is listed nowhere.** garam omits it from `listDefinitions`, and control releases nothing of it.
- **An imported agent cannot be configured until its switch.** That keeps every revision before the switch unreleased.
- **The legacy agent needs a certificate taken before the switch.** garam's legacy certificate route refuses the agent's operator from the switch on.
- **The manager does not take a GRN over yet.** Until the manager slice reads `origin`, its renderer keeps refusing a `Garam`-source `Agent` the feed names, and logs it.

Ruled out:
- **Importing `tools.pins.*` automatically.** It could change what runs.
- **A null reference for revision 1's first activation.**
- **One authority for the switch's two operations.** garam binds one request id to one operation.
- **Learning the switch from anything but control's own call to garam.** Control is the caller, and acts on garam's answer alone.
