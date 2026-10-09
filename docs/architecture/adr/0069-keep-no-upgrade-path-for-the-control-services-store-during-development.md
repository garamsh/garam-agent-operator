# ADR 0069: Keep no upgrade path for the control service's store during development

> Status: accepted
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The owner directed on 2026-10-09 that during development this repository keeps only the correct structure, with no path kept for an earlier version of itself: "옛 버전까지 고려할 필요 없어. 옳은 구조만 남기는게 개발 버전에서 목표야" (#327). [ADR 0068](0068-keep-no-backward-compatibility-path-during-development.md) applied that to the manager, and kept the control service's upgrade path for a pull request of its own (#329).

That path has three parts.

- **The schema's history, and adoption ([ADR 0055](0055-change-the-control-services-schema-only-by-forward-only-versioned-migrations.md) §2 to §4).**
  - Migration 1 is the schema the published `7c216469476d` created.
  - Migration 2 carries it to the organization-scoped schema, reading each earlier row's organization from what it carries, and moving a row with no source into `creations_n1`, `templates_n1` or `profiles_n1`.
  - Migrations 3 and 4 add recoveries, stops and a recovery's chain.
  - A database with no `schema_migrations` is adopted at version 1 or 2 when its catalog matches what those migrations build, and refused, naming the difference, otherwise.
- **The archived creation.** [ADR 0058](0058-refuse-the-first-certificate-of-an-agent-whose-only-creation-the-upgrade-archived-and-name-its-recovery.md) refuses `409 creation_archived` the first certificate of an agent whose only creation is in `creations_n1`. [ADR 0063](0063-keep-an-agent-whose-only-creation-the-upgrade-archived-through-credential-recovery-and-a-configure.md) names credential recovery, then a configure, as the route that keeps such an agent.
- **The upgrade-only tests and their fixtures.**
  - **The control service's.** `tests/control/migration_test.go`'s upgrade and adoption cases, `archived_creation_test.go` and `archived_recovery_test.go`, `internal/distribution/archived_creation_test.go`, and `test/testdata/schema-7c21646` and `schema-7b31d01`.
  - **The CRD's.** `internal/controller/agent_crd_upgrade_test.go`, `internal/garam/constructor/crd_upgrade_test.go` and `test/testdata/crd-7c21646`, which store an `Agent` under the CRD at `7c21646` and install the current one.

No deployment runs the control service on data that must survive. gitops does not deploy it, and garam's S8 harness builds it fresh, so there is no production data to keep (#329).

## Decision

**The control service's store keeps no path from an earlier version of itself.**

- **Migration 1 is the whole current schema.** The four earlier migrations are squashed into one `000001_schema.up.sql`. It builds what migrations 1 to 4 built: the same tables, columns, types, nullability, defaults, keys, constraint and index definitions, and the same `positions` row. Only some constraint names differ. Migration 2 created `creations`, `definitions`, `profiles` and `templates` beside their renamed predecessors, so their NOT NULL and CHECK constraints took suffixed names. The squashed migration gives them the names a fresh table gets. Nothing reads a constraint by name.
- **The migration mechanism stays as ADR 0055 decided it,** so that a later change still goes forward as a new migration:
  - numbered, forward-only migrations, embedded and applied at start through `golang-migrate`'s `pgx/v5` driver;
  - the binary's own advisory lock, held from reading the schema to its last migration;
  - the refusal, touching nothing, of a database a newer binary migrated, and of one a failed migration left dirty.
- **A database with no `schema_migrations` is migrated only when it holds no table.** One that holds any table is refused, touching nothing, with a message telling the operator to recreate it empty. Nothing compares its shape, and nothing adopts it. This supersedes ADR 0055 §3, adoption, and §2's refusal of an unknown shape by its difference from the nearest version.
- **No migration archives.** `creations_n1`, `templates_n1` and `profiles_n1` exist in no schema, and nothing logs or reads an archive. This supersedes ADR 0055 §4.
- **An agent with no creation gets no first certificate,** answered 404 as every such agent already was. `ErrCreationArchived`, `409 creation_archived`, `Repository.ArchivedRegistration` and its query are removed. This supersedes ADR 0058 entirely, and ADR 0063, whose subject was an agent only ADR 0058's archive could hold.
- **The upgrade-only tests and fixtures go,** the CRD's with the control service's. What they measured stops being claimed.

**What stays, with its reason:**
- **Credential recovery ([ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md), [ADR 0062](0062-write-the-chain-garam-answers-a-recovery-with-and-verify-the-recovered-leaf-against-it.md)).** It recovers any agent's credential, not only one an upgrade stranded.
- **Enrolment, certificates, the placement token and mTLS.** They are untouched here, because garam's auth model is about to change (`garamsh/garam#1251`, ADR-0100, proposed).

## Consequences

- **A control database an earlier development build made is not upgraded.** One that build migrated records version 2, 3 or 4, so this binary refuses it as migrated by a newer binary, touching nothing. One made before migrations holds tables with no `schema_migrations`, and is refused too. Either is recreated empty.
- **A database dirty at version 1 is cleared with `DELETE FROM schema_migrations`,** as before, which the refusal states.
- **The next schema change is migration 2.** It is written against migration 1 as it stands, and nothing earlier.

## Rejected alternatives

- **Keep the four migrations and drop only adoption and the archives.** Migration 2 exists to carry `7c216469476d`'s rows forward, and a fresh database would still run that carrying-forward over empty tables.
- **Squash to version 4, so that a database at version 4 starts unchanged.** That keeps a path for an earlier build, which the directive removes.
