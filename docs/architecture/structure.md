# Structure

How this repository is partitioned: what a unit is, what may reference what, and when a new unit is earned. The shape is domain partitioning, chosen in [ADR 0038](adr/0038-partition-the-code-by-domain.md).

## Current decisions

### The unit

- **A domain is the unit.** One package per domain, named for its subject matter and never for a technical role. `internal/definition/` (the control service's desired state) and `internal/garam/` (this operator's dealings with `garam`) are domains.
- **A folder named for what it does to code rather than what it holds is not a domain** — `utils`, `helpers`, `common`, `shared`, `misc` and their kind. Where two pieces of code share a concept, the concept gets a name and that name is the folder.
- **Every file belongs to exactly one domain.** A file that would belong in two means the split is wrong, and so does a cycle between domains.

### What the framework places

The kubebuilder scaffold writes the manager's code into paths it names, and the CLI rewrites them. Those paths stay where it puts them, even where their names are a technical role:

- `api/<version>/` holds the API kinds. It sits **below every domain**: a domain may import it, and it imports no domain.
- `internal/controller/` holds the reconcilers. It is the domain of the `Agent` workload — the Pod, its volumes and its status — under the name the scaffold gives it.
- `test/utils/` is scaffolded test support. It is no domain, and it is not a precedent for a `utils` anywhere else.

### Surfaces and references

- **A domain is reached only through what it declares public**: the exported identifiers of its root package, `internal/<domain>`. Its sub-packages (`internal/<domain>/<pkg>/`, the implementations of its dependency interfaces) are its inside. Reaching one from another domain is a defect, even where the compiler permits it.
- **A domain may depend on a sibling, through that sibling's surface, in one direction only.** That is the side taken in ADR 0038. A reference two domains would make of each other means one of them is holding something that belongs to the other, or below both.
- **A consumer depends on the producer's interface, not its concrete type.** A domain declares beside itself the behaviour it needs. The behaviour it offers is declared beside its producer. The compiler does not check this, so it is a review matter.
- **The composition site is the exception.** A binary's composition site — `cmd/main.go` for the manager, `cmd/control/main.go` for the control service — constructs every domain's concrete implementations and passes them to the interfaces. It is the one place that reaches inside a domain.

### Shared code

- **What two domains both need sits below them, never beside them.** It is extracted to a package named for what it is, which imports no domain, or it is owned by one domain and reached through that domain's surface. It never becomes a sibling folder that no domain owns.

### A new domain

- **A new domain is earned** by subject matter that has its own lifecycle and vocabulary, that something outside it depends on, and whose public surface can stay stable while its inside changes. Code that fails any of the three belongs in an existing domain.

## Rationale

[ADR 0038](adr/0038-partition-the-code-by-domain.md) records the choice of domain partitioning against the layered and hexagonal alternatives. It also records the side taken on sibling dependencies and what would show the choice was wrong.

## Open questions

- **How the control service's API is partitioned.** Issue #211 adds HTTP routes over `internal/definition`. Whether that HTTP surface is part of the domain or a domain of its own will be settled when the routes land, against §A new domain.
