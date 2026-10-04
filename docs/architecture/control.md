# Control

The control service's desired state: agents' execution definitions and their revisions, templates, safe execution profiles, creation requests, and where they are stored.

## Current decisions

- **The control service is a second binary, and its desired state is `internal/definition/`.** It follows `stack-go.md` alone, as `docs/convention/README.md` §Stack-specific splits a binary that is not the manager. Nothing in the manager imports it.
- **The binary serves health and nothing else yet.** `cmd/control/main.go` opens its store from the connection URL in `CONTROL_DATABASE_URL`, which is an environment variable rather than a flag because it carries a password. It applies the schema and serves `/healthz` and `/readyz` on `--health-probe-bind-address` (default `:8081`). `/readyz` answers only while the database answers a ping. Any failure to open the store or apply the schema stops the binary. No API is served and garam is not called.
- **Its image is built from `build/control.Dockerfile`**, by `make docker-build-control`, with the repository root as context. Where it is published is `delivery.md`.
- **The control service's image stays separate from the manager's.** The manager runs in a customer's cluster and holds the cluster controller's credential. The control service runs where this project's operators of the service deploy it, and the two hold distinct credentials, which `garamsh/garam#1155` §2 requires. One image would carry both programs to both places.
- **Each agent has one desired definition, keyed by its GRN, held as revisions.** Revision 1 is stored only when garam registers the agent's creation. Every later change appends the next revision and earlier ones are kept. A change states the revision it was based on; one based on any revision but the latest is refused with `ErrStaleRevision`, and nothing from it is merged. A change to an agent with no revision is refused with `ErrNotFound`.
- **A definition carries the agent's configuration and names a profile version.** The configuration is the model (provider, base URL, model name, and a reference to its API key), the ego text, and the tool pins, one per tool, each opaque here as `agent.md` records. A secret appears only as a reference to where it is held.
- **A template is a named configuration and profile version, published in numbered versions and never modified.** Publishing a name again is its next version. Creating an agent copies the template version it names into revision 1 once; a later version of the template never reaches an agent created from an earlier one.
- **A profile is a named set of execution settings, published in numbered versions and never modified.** The settings are the workload's resources, its storage size and its storage class. A template or a definition naming an unpublished profile version is refused with `ErrNotFound`.
- **The container image is neither a profile nor a definition field.** It stays the operator's own configuration, for the security reason in ADR 0007.
- **A creation is identified by its actor, organization and request id, and ends at most once.** It is stored `Pending` before garam is asked, then becomes `Registered` with the GRN garam minted or `Failed` with garam's refusal. A repeated request returns the stored outcome and never stores a second creation. One still `Pending` asks garam again, which `Registrar` requires to answer one key with one GRN. An error that is not a refusal leaves the outcome unknown, so the creation stays `Pending`. A repeated key naming a different template is refused with `ErrRequestReused`.
- **garam's registration is the `Registrar` interface, with no implementation yet.** Its wire contract is still being agreed (`garamsh/garam#1155` §2).
- **The store is `Repository`, and each rule's atomicity is the store's.** Appending a revision checks the latest in the same write, a creation is inserted or the stored one returned in one step, and a registration stores the creation's outcome and revision 1 together. `internal/definition/repository/memory.go` holds it in process for tests and development.
- **The persistent store is a PostgreSQL database of the control service's own, through pgx v5. Its schema is built and its data methods are not.**
  - **Schema.** `internal/definition/repository/schema.sql` is embedded in the binary, which applies it at every start through `Postgres.ApplySchema`. Every statement in it only creates what is missing.
  - **Constraints.** A second revision under one (agent, revision) is refused by that key, and a second creation under one (actor, organization, request id) by that one. These are the constraints the stale-revision and duplicate-creation rules rest on.
  - **Data methods.** The PostgreSQL implementation of `Repository` lands with issue #211's first slice, where the binary serves the routes that create and update. Its code waits on the branch `feat/control-store-data`.
- **Domain behaviour is tested on the in-memory store**, in `internal/definition/*_test.go`. `testing.md` keeps a real database out of the integration layer.
- **What the e2e layer verifies of the store today is what the built binary produced.** `tests/control/` builds `cmd/control`, runs it against a PostgreSQL container that testcontainers-go starts, and reads the database the binary set up:
  - the schema's tables exist once `/readyz` answers;
  - a second revision under one number is refused beside an accepted first;
  - a second creation under one key is refused beside an accepted first and a different key.
  - `make test-e2e-control` runs it, and `make test-e2e` runs it first.
- **The behaviours the in-memory store's tests cover arrive at the e2e layer with #211**, tested through the binary's routes: a stale revision refused, concurrent updates on one revision storing one, a repeated creation returning the first outcome, and concurrent repeats of one creation storing one.

## Rationale

The ownership — definitions, templates and revisions here, the GRN and authorization in garam — is `garamsh/garam#1155` D1 and D2, recorded in this repository as ADR 0032 (issue #205). The revision, copy-once and idempotent-creation rules are that design's console and creation contracts (§2) restated as what this service stores.

The store is [ADR 0036](adr/0036-persist-the-control-services-desired-state-in-its-own-postgresql-database.md): each rule above is one constraint or one transaction in PostgreSQL, and the owner's hosted services already run it.

A creation stays `Pending` on an unknown outcome rather than failing, because a timeout is not a refusal (`garamsh/garam#1155` §2, "timeout is unknown outcome"): failing it would let a retry under a new request id register a second agent for one intent.

## Open questions

- **What moves the e2e suite's PostgreSQL image.** It is pinned by digest in `tests/control/main_test.go`, which no Dependabot ecosystem reads, so it is moved by hand.
- **How the schema changes once a table holds rows.** `schema.sql` creates what is missing and alters nothing, so a change to an existing table needs a migration that no part of the binary performs yet.
- **What configuration the domain refuses.** It stores any configuration it is given, including an empty tool-pin set, which `agent.md` records `sherlock` refusing. Where that check belongs waits on the boundary that parses a request, which is not built.
- **Who may publish a profile or a template.** `garamsh/garam#1155` D4 makes editing a profile a high-trust action; nothing here checks an actor yet.
