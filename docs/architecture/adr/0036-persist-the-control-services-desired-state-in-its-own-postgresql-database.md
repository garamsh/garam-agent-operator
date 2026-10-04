# ADR 0036: Persist the control service's desired state in its own PostgreSQL database, through pgx v5

> Status: accepted
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The control service owns agents' desired execution definitions, the templates they are created from and the safe execution profiles they run with (`garamsh/garam#1155` D1 and D2, accepted 2026-10-04; ownership in this repository is ADR 0032, issue #205). Issue #207 builds its domain, `internal/definition/`, and leaves the store's technology to this record.

Three of the domain's rules are only as safe as the store's writes are atomic:

- **A stale update is refused.** Two edits based on one revision both compute the same next revision; exactly one may be stored.
- **A repeated creation request returns the first outcome.** Two arrivals of one (actor, organization, request id) must leave one record.
- **A registered creation and the agent's first revision are stored together.** One without the other is an agent garam knows and this service cannot render, or a definition no creation accounts for.

A uniqueness constraint and a transaction give each of these, so the store has to offer both. Alternatives weighed:

- **SQLite on a volume.** Has both, but ties the hosted service to one writer process and one volume, and the owner's hosted services already run PostgreSQL (issue #207).
- **Kubernetes objects.** `garamsh/garam#1155` D2 rejects CRDs as the managed authority: the hosted service is not in the customer's cluster.
- **garam's database.** `garamsh/garam#1155` §2 gives garam no desired runtime values; sharing its database shares the ownership D2 separates.

## Decision

The control service stores its desired state in a PostgreSQL database of its own, which no other service reads or writes, through `github.com/jackc/pgx/v5` with `pgxpool`. The PostgreSQL implementation of `definition.Repository` sits beside the in-memory one in `internal/definition/repository/` and lands with the control binary in issue #208, where the e2e suite against the built binary exercises it.

The pin was checked on 2026-10-04, per `ci.md` §Verify a pinned dependency:

- **Version:** `v5.11.0`, the `@latest` that `proxy.golang.org` reports, published 2026-09-07 from tag `v5.11.0` at `5e583fa7aabfa88b796292f849fc9d7d75ac159d`. Its `go.mod` states `go 1.25.0`, below this module's `go 1.26.0`.
- **Usage:** `pgxpool.New` with a PostgreSQL connection URL, as `README.md` at that tag shows.
- **Advisories:** OSV returns ten records for `github.com/jackc/pgx/v5`, GitHub and Go-database identifiers for overlapping advisories; each is fixed at or before `v5.9.2`.

Issue #208 adds the module to `go.mod`, so it checks the version again on the day it does.

## Consequences

Easier: each of the three rules above is one constraint or one transaction, so the PostgreSQL implementation and the in-memory one can be held to one behaviour without either taking a lock the other lacks.

Harder: the hosted service needs a database provisioned and a credential for it, which is environment configuration this repository does not hold.

Ruled out: the control service reading garam's database, and any second process writing this one.

Not decided here: what moves the pin. `.github/dependabot.yml` names `github-actions` and `docker` and no `gomod` entry, so no Go module in this repository has a mover today; `control.md` §Open questions carries it.
