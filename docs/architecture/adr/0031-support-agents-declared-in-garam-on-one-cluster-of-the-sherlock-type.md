# ADR 0031: Support agents declared in garam, on one cluster, of the sherlock type, and keep a hand-written Agent to development

> Status: accepted
> Date: 2026-09-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The owner set this operator's supported scope on 2026-09-28, from the service's point of view, on issue #202. Most of what the scope rests on is already decided, and this record cites it rather than restating it:

- [ADR 0007](0007-claim-definitions-from-a-poller.md) divides a definition, which says what an agent is and which `garam` holds, from this operator, which claims it and builds it. That division rests on `garam`'s ADR-0026, "the console defines an agent" (`garam@8f9dd9d:docs/architecture/adr/0026-the-console-defines-an-agent.md:14`).
- [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md) constructs an `Agent` for every definition this operator holds a claim on, and places the credential the certificate route issues for it.
- [ADR 0016](0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md) is how `garam` learns of an agent's pod: this operator reports it, fenced by the epoch the claim proved, and `garam` dials no cluster.
- [ADR 0025](0025-generalize-the-agent-kind-by-agent-type.md) and [ADR 0029](0029-route-every-agent-specific-name-in-the-pod-through-its-types-descriptor.md) admit `claude-code` and `codex` at the API and refuse them on reconcile with `TypeUnimplemented`, because no descriptor for either is filled in.

What none of them says is which of the shapes they allow is the one supported. The code reconciles a hand-written `Agent` wherever it lives (`delivery.md`, issue #161), builds any admitted type it has a descriptor for, and polls one `garam` address per deployment with nothing preventing a second deployment elsewhere. Each of those reads as supported until something says otherwise.

The alternative considered for the first point was to declare `Agent` objects in the cluster and have `garam` observe the cluster. It was not taken, because it moves three things out of `garam` that `garam` holds now:

- **Identity.** The agent GRN is minted by a definition; an `Agent` written in a cluster has none.
- **Per-agent certificate issuance.** The certificate route answers for an agent a claim admits this operator to (ADR 0009); an `Agent` nothing claimed has no route to a certificate.
- **Single ownership.** The claim and the epoch it carries (ADR 0016) are what make one operator the holder of an agent; a cluster-declared `Agent` would have to reinvent both, and `garam` would need Kubernetes API credentials to observe it.

## Decision

1. **Agents are declared in `garam`.** A definition in `garam` is the supported way an agent comes to exist; this operator claims it and builds it, as ADR 0007 and ADR 0009 already decide. Nothing in those decisions moves.
2. **One cluster is supported, for now.** An enterprise deployment is one cluster running one operator. The pull-and-claim mechanism stays as built — it works in production end to end, keeps `garam` free of Kubernetes API credentials, and costs nothing with one operator. Not supporting several clusters is a statement of scope and not a design that prevents them: nothing here is removed or narrowed to one.
3. **`sherlock` is the one supported agent type; the others are on hold.** ADR 0025 and ADR 0029 stand as written: `claude-code` and `codex` stay in the `v1alpha1` enum, admitted by the API and refused on reconcile with `TypeUnimplemented`. Nothing is removed from `v1alpha1`.
4. **A hand-written `Agent` is for development and bring-up only.** It is reconciled wherever it lives, as `delivery.md` records, but it has no GRN, no certificate `garam` issued for it, and nothing reports it to `garam`, so `garam` does not see it. `agent-sample` in the `gagent-bringup` namespace, since removed (`garamsh/gitops#261`), was one.

This supersedes nothing. Each point either restates the scope of a decision already taken or records that a mechanism already built is not a supported path; no decision in ADR 0007, 0009, 0016, 0025 or 0029 changes.

## Consequences

Easier: a report of a problem outside this scope — a second cluster, a `claude-code` agent, a hand-written `Agent` `garam` does not list — has an answer that is not a bug: it is outside what is supported.

Harder: nothing enforces the scope. A hand-written `Agent` still builds a workload, a second operator enrolled with the same `garam` still polls the definitions naming it, and an admitted type still reaches the reconciler before it is refused. Enforcing any of these — restricting reconciliation to claimed `Agent`s, or removing a type from the API — is out of scope here and would be its own decision.

What would reopen each point:

- **Agents declared in `garam`:** a case where an agent must exist that `garam` cannot define, which would first have to answer where its identity, its certificate and its single owner come from.
- **One cluster:** a deployment that needs a second cluster. The mechanism already allows one; what is unmeasured is two operators claiming against one organization's definitions in production.
- **`sherlock` only:** a second agent type with a settled contract — the environment and file names ADR 0029's descriptor needs, published by that agent's project.
- **A hand-written `Agent` for development only:** a production use of one — an `Agent` that must run for a user without a definition in `garam` behind it.
