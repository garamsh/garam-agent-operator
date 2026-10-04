# ADR 0038: Partition the code by domain, and let a domain depend on a sibling through its surface in one direction

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #222. The conventions template moved its structural rules out of `stack-go.md` and into a per-project `structure.md` (`convention-driven-project#382`, `#394`, `#395`). The rules moved were unit names, references between units, and where shared code goes. A project chooses one shape from the template's `docs/architecture/structures/` set and records the choice. This repository had made no choice: [ADR 0004](0004-extend-stack-go.md) took `stack-go.md`'s layout whole. Without a `structure.md`, the sync would leave the control service, which `docs/convention/README.md` assigns to `stack-go.md` alone, with no rule about units at all.

The code already had a shape when the choice was made, at `dev` 483dc41:

- three subject areas with their own vocabulary and lifecycle: `internal/definition` (definitions, revisions, templates, profiles), `internal/garam` (enrollment, credentials, claims, construction) and `internal/controller` (the `Agent` workload);
- one of them, the control service's `internal/definition`, already ships as a separate binary and image (ADR 0033);
- each external technology — the Kubernetes API, `garam`, PostgreSQL — has exactly one real counterpart.

The three candidates, read side by side:

- **Layered** fits one subject area, or a subject not yet understood well enough to partition by. Neither holds here. A feature such as "an agent's identity" (ADR 0037) would be spread across every layer.
- **Hexagonal** earns a port by a conversation whose technology is substituted. The statement expects two or more adapters on a port. Here no technology has a second adapter apart from the in-memory store, and that one serves tests. Ports over the Kubernetes client and `garam`'s client would be indirection with one implementation each.
- **Domain** fits subject areas that the people asking for the work already name, changes that mostly land in one area, and an area that may ship separately, which the control service already does.

Domain partitioning leaves one point to the project: whether a domain may depend on a sibling at all. The range runs from Shared Kernel to Separate Ways. Separate Ways cannot hold here. `internal/garam` constructs `Agent` objects, and `internal/controller` reads the Secret that construction writes, so two domains share facts that neither can stop knowing.

## Decision

**The code is partitioned by domain**, as `structure.md` states. **A domain may depend on a sibling only through the sibling's declared surface, which is the exported identifiers of its root package, and only in one direction.** The API kinds in `api/<version>/` sit below every domain, where the kubebuilder scaffold places them.

## Consequences

- `structure.md` carries the structural rules. `stack-go.md` and `stack-kubebuilder.md` keep only how those rules are spelled in Go and what the scaffold imposes (`docs/architecture/README.md` §Rules 6 and 8).
- **One existing reference becomes a defect, which a follow-up issue tracks.** `internal/controller` imports `internal/garam/constructor` for the credential Secret's key names. That sub-package is the inside of `internal/garam`. The names move to `internal/garam`'s root package, which both the constructor and the controller then reference.
- The control service's API (#211) chooses, when it lands, whether its HTTP surface is part of `internal/definition` or a domain of its own, against `structure.md` §A new domain.
- Ruled out: the template's `structures/` folder in this repository. It is the set the choice was made from, and keeping it would leave two unchosen shapes reading as conventions.

## Signs the decision was wrong

From the template's `structures/domain.md` §Signs the choice was wrong, as they would appear here:

- Most changes touch several domains at once, and the pair that always moves together cannot be released independently — for example, every change to `internal/garam` also changes `internal/controller`.
- The same fix is applied once per domain, and a domain keeps being missed.
- The shared code every domain imports is growing faster than any domain.
- Which domain a new file belongs to cannot be settled without asking someone.
- A domain's public surface has grown until it names what the domain keeps inside.

Specific to the sibling rule: the one-way references keep needing a callback or an event to point back. That means the two domains are one, or something between them is a third.
