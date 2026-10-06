# ADR 0055: Change the control service's schema only by forward-only versioned migrations, keeping every earlier row

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0036](0036-persist-the-control-services-desired-state-in-its-own-postgresql-database.md) gives the control service its own PostgreSQL database. Until this change the binary applied an embedded `schema.sql` at every start, and every statement in it only created what was missing. A change to an existing table was an edit to that file, which a database already holding the table never saw. That was safe only while no database was deployed.

Issue #292 is where that stopped holding:
- **What is deployed.** The release `7c216469476d` is published, and a deployment holds the database it created, with rows.
- **What has changed since.** The schema has changed in place since then: `organization` on `profiles`, `templates` and `definitions`; the bound fields, controller, profile and epoch on `creations`; the activation columns on `agent_status`.
- **What breaks.** A later binary's `schema.sql` fails on that database at start, because an index it creates names a column the earlier table lacks, so the binary never becomes ready.

The PM settled the decision below on #292's dispatch, including the owner's condition that no migration is destructive. Two cases were then put to the PM as questions and settled before they were bound: §4's archives (Q1, Q2) and §3's adoption at version 2.

## Decision

1. **The schema changes only by numbered, forward-only migrations.**
   - **Where they are.** They are `internal/definition/repository/migrations/NNNNNN_<name>.up.sql`, embedded in the binary.
   - **How they run.** At start, before it serves, the binary applies every pending one in order. It does so through `golang-migrate` v4.20.1's `pgx/v5` driver, which takes a PostgreSQL advisory lock, runs each file in one transaction, and records the version reached in `schema_migrations`.
   - **The second lock.** The binary holds an advisory lock of its own from reading the schema to its last migration, so two binaries starting on one database never both decide to adopt it.
   - **Once merged.** A migration is never edited, and a schema change is a new file. `schema.sql` is deleted.
   - **No down migrations.** No `.down.sql` is written.
   - **The shape is garam's.** File naming and the dirty-database refusal follow garam's own migrations (`garamsh/garam@59fe68dca4f5f0b42e6f159eba324db0c65486d9`, `migrations/000001_*.up.sql` to `000034_*`, and `internal/cli/migrate.go`, golang-migrate v4.19.1). Garam also ships `.down.sql` files, 32 of them beside its 34 migrations, and that part is not taken.
2. **What the binary refuses, touching nothing.** In each case it exits with a stated message.
   - **A newer database.** A database recorded at a version above the newest it embeds was migrated by a newer binary. The message names both versions.
   - **A dirty database.** A database recorded as dirty is one where a migration failed. Its transaction kept nothing, and the message says which version to force once the schema is known.
   - **An unknown shape.** A database with no `schema_migrations` table whose schema matches no adoptable version is refused, and the message lists what differs from the nearest one.
3. **A database made before migrations is adopted when its schema exactly matches a version's.**
   - **Exactly** means the same tables, the same columns with the same types and nullability, and the same primary key, foreign key, unique and check constraints and indexes. Constraint and index names are not compared.
   - **How it is compared.** It is compared through the catalog against the schema the migrations up to that version build in a scratch schema, inside a transaction that is rolled back. It is never compared through DDL text.
   - **Version 1** is the schema `7c216469476d` created (`git show 7c21646:internal/definition/repository/schema.sql`), which migration 1 repeats verbatim.
   - **Version 2** is the schema the last `schema.sql` created (`7b31d01`), which is what migration 2 produces. A dev build's database, such as the lab's made by `8e11169`, is carried this way.
   - **An empty database** is migrated from version 1.
4. **No migration drops a row an earlier version stored, or truncates a table.** A column an earlier row lacks is filled from what the row already carries, and is never guessed:
   - **Definitions.** A definition's organization is its agent's GRN's organization segment, `grn:<organization>:...`. A GRN with none refuses the migration, since a definition is an agent's desired state and is not archived.
   - **Templates.** A template's organization is the organization of each creation naming it.
   - **Profiles.** A profile's organization is the organization of each template or definition naming it.
   - **More than one organization.** A template or profile named from several organizations is copied once into each, and the original key is kept as one of the copies.
   - **Creations.** A creation's profile is its template's. `conflict` is false. `agent_status`'s new columns are null, which they allow.
   - **A row with no source** for a column the schema now requires moves, unchanged, into an archive that nothing reads, in the same transaction:
     - **Creations (Q1).** Every earlier creation lacks the bound fields, the controller and the epoch, so it moves into `creations_n1`.
     - **Unnamed templates and profiles (Q2).** A template or profile that nothing names has no organization, so it moves into `templates_n1` or `profiles_n1`.
     - **What an archive holds.** It has every earlier column, plus the migration that moved the row and when, and is created only where a row moves into it. Each moved row is logged at WARN with its key.
   - **The final check.** Before an earlier table is dropped, every one of its rows is found in its successor or its archive. Otherwise the migration fails, and its transaction keeps nothing.
5. **`golang-migrate/migrate/v4` v4.20.1 is pinned by `go.mod` and `go.sum`.** It was checked as `ci.md` §Verify a pinned dependency asks:
   - it was the latest version on `proxy.golang.org` on 2026-10-07 (published 2026-09-09, tag commit `504568a3cbd23b8754760f55a3d89aec1b0c4963`);
   - its `go.mod` declares `go 1.25.11`;
   - OSV (`api.osv.dev/v1/query`, ecosystem Go, version 4.20.1) lists no vulnerability for it;
   - its `database/pgx/v5` README documents the advisory lock, the `schema_migrations` table and the single-statement transaction used here.

   Adding it moves `golang.org/x/time` from v0.14.0 to v0.15.0, which it requires. Dependabot's `gomod` entry moves it, as it moves every module.

## Consequences

- **The published release's database starts under the current binary.** Its rows are kept, and its earlier creations are archived rather than lost, because #292's binary could not have read them. Re-publishing an archived template or profile under its organization waits for a supported way (#294).
- **A database a dev build made between `7c21646` and `7b31d01`** that matches neither version is refused. Only an unpublished build could have made one.
- **A failed migration needs an operator.** The database is left dirty at that version with nothing of the migration kept. The binary refuses it until the recorded version is forced back, as garam's `migrate` does.
- **A binary never runs on a schema newer than it knows.** Rolling a deployment back past a migration therefore means restoring the database from before it.
- **Ruled out.**
  - **Editing `schema.sql` in place.** That is the defect #292 measured.
  - **Down migrations.** A forward-only path needs no rollback that could drop rows.
  - **Filling the unknown creation fields with `''`.** That is a stand-in that later reads as data (Q1, option C).
  - **Refusing an upgrade on every hand-written creation** (Q1, option A).
  - **An owners table the operator fills in for unnamed templates and profiles.** That would be a second mechanism and a manual gate for the class of row the archive already settles (Q2).
  - **Refusing a database that already matches migration 2's result.** Adopting it changes nothing, and refusing it would turn a correct state into a manual step.
