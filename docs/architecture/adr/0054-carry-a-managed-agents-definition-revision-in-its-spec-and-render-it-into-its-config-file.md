# ADR 0054: Carry a managed agent's definition revision in its spec, and render it into its config file

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #291, found in Garam's joint kind run (garamsh/garam#1196, scenarios 1, 4 and 5 of garamsh/garam#1157): no managed agent can activate.

**What the activation needs.**
- garam's adapter binds the activation to the `config_revision` the agent reports (`garam@59fe68d:internal/delivery/http_authority.go:78`).
- This project's activation route reads `configRevision` with `definition.ParseRevision` (`internal/execution/activation.go:202-215`). An empty one is refused `400 invalid_request` ([ADR 0048](0048-serve-the-agent-execution-routes-from-an-execution-domain-under-garams-contract.md), `control.md` §Agent routes).

**What `sherlock` reports.** At `sherlock@b3c05c24bc2a346521251556935bfe0dc70f8cd1` (`v0.2.0`):
- it reads a top-level `revision` key from its config file as an opaque string, empty where the key is absent (`internal/config/config.go:80-84`);
- it reports that string verbatim as `config_revision` and never computes one (`cmd/sherlock/agent.go:281`, `internal/gateway/gateway.go:38-40`).

**What this operator rendered.** [ADR 0043](0043-pull-managed-agents-desired-state-from-the-control-service-and-keep-each-grn-on-one-source.md) lists the fields the renderer writes into a `Control` `Agent`, and the revision is not one of them. `sherlockConfig` had no revision. So every managed agent reported `config_revision: ""`.

**The image the lab pins.** `sherlock@837b80afe2729230daeab21621f3f97cabb6dfb4` has no `revision` setting: its `Config` struct (`internal/config/config.go:48-77`) has no such field, and nothing at that commit reports a config revision. Its loader warns on a key no setting reads and does not fail (`reportUnknownKeys`, `internal/config/config.go:321-337`), as [ADR 0052](0052-carry-an-agents-embeddings-endpoint-beside-its-model-and-refuse-changing-it-once-set.md) records.

The PM accepted the field's place and shape on this dispatch.

## Decision

**The `Agent` carries the revision in `spec.revision`.** It is the control service's definition revision the spec was rendered from: the feed's `revision` for that agent, copied verbatim.
- **The renderer writes it.** It writes the field on every render of a `Control` `Agent`, as it writes every other field ADR 0043 lists. That includes a cutover's first render. The value is never inferred and never counted by the manager.
- **The form.** It is a canonical decimal string, admitted by the pattern `^[1-9][0-9]*$` and at most 19 characters. That is the form the C2 wire carries (ADR 0040) and the activation route takes.
- **The `Control` source only.** A CEL rule on `AgentSpec` refuses `spec.revision` unless `spec.identity.source` is `Control`. An `Agent` on the `Garam` source, or with no identity, has no definition revision.

**The controller renders it as `sherlock`'s top-level `revision` key**, a YAML string.
- **Where none is set.** The key is left out, so a `Garam`-source agent's config file is what it was before this decision.
- **An `Agent` declaring only a revision** is still given a config file.

**Compatibility.**
- **`Agent`s stored before the field.** None of them carries the field, and the new rule admits any spec without one, so each is written as before. `internal/controller`'s CRD upgrade test stores both sources under the CRD at `7c21646` and writes them under this one. A `Control` one takes the field at its next render.
- **`sherlock@837b80a`** starts with the key and logs "unknown config key: nothing reads it" for `revision`. It reports no revision, so an agent on that image still cannot activate. Activation needs `v0.2.0` or later.

## Consequences

- **A managed agent reports the revision it was started with.** Its adapter's activation binds to a revision the route accepts. After a configure, the new revision reaches the config file through the next render, and `effective.revision` follows once the agent reports it.
- **Every new revision rolls the agent's Pod**, including one that changes nothing else the agent reads. The revision is in the config file's text, which is in the Pod template. A revision the agent was not restarted under is one it cannot report, so the restart is what the decision exists for.
- **A hand edit to `spec.revision` is reverted at the next revision**, as for every rendered field.

Ruled out:
- **An annotation the renderer owns.** It is untyped, so nothing admits only the canonical form or only the `Control` source. ADR 0043 refused an annotation for the source on the same ground.
- **A field under `spec.identity`.** Identity is who the agent is in garam, and the epoch it was proved at. The revision describes the rest of the spec instead: which definition produced it.
- **A counter the manager keeps, or a revision derived from the spec's content.** Neither is the definition revision the control service stored, which is what the activation route compares against.
