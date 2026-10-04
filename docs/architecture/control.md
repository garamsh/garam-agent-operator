# Control

The control service: agents' execution definitions and their revisions, templates, safe execution profiles, creation and configure requests, where they are stored, and the console API that changes them.

## Current decisions

- **The control service is a second binary, and its desired state is `internal/definition/`.** It follows `stack-go.md` alone, as `docs/convention/README.md` §Stack-specific splits a binary that is not the manager. Nothing in the manager imports it.
- **The binary serves health and the console's configure route.** `cmd/control/main.go` opens its store from the connection URL in `CONTROL_DATABASE_URL`, which is an environment variable rather than a flag because it carries a password, and applies the schema.
  - **Health.** `/healthz` and `/readyz` are served on `--health-probe-bind-address` (default `:8081`). `/readyz` answers only while the database answers a ping.
  - **Console routes.** These are served on `--api-bind-address` (default `:8080`), over TLS 1.3 and nothing else.
    - **Certificate.** The binary terminates TLS itself, under the certificate chain in `--api-certificate-file` and the key in `--api-key-file`. It refuses to start without both.
    - **Why.** Every request carries a bearer operation authority. Plaintext would hand that authority to anyone on the path for the five minutes it lives, and the body digest bounds only what it can send, not who sends it.
    - **Plain HTTP.** A plaintext request to the port is answered by the TLS listener and never reaches a route.
    - **Health.** The health listener stays plain HTTP: it carries no credential.
  - **garam.** The binary calls garam's machine listener at `--garam-machine-url` over mutual TLS. It presents the operator certificate in `--operator-certificate-file` and `--operator-key-file`, and verifies garam against `--garam-server-root-file`. The certificate's one SAN URI is this service's operator GRN, the audience every authority must name.
  - Any failure to read those files, open the store or apply the schema stops the binary.
- **Its image is built from `build/control.Dockerfile`**, by `make docker-build-control`, with the repository root as context. Where it is published is `delivery.md`.
- **The control service's image stays separate from the manager's.** The manager runs in a customer's cluster and holds the cluster controller's credential. The control service runs where this project's operators of the service deploy it, and the two hold distinct credentials, which `garamsh/garam#1155` §2 requires. One image would carry both programs to both places.
- **Each agent has one desired definition, keyed by its GRN, held as revisions.** Revision 1 is stored only when garam registers the agent's creation. Every later change appends the next revision and earlier ones are kept. A change is a configure request: it states the revision it expects to replace, and one expecting any revision but the latest is refused with `ErrStaleRevision`, with nothing from it merged. A configure request for an agent with no revision is refused with `ErrNotFound`.
- **A definition carries the agent's configuration and names a profile version.** The configuration is the model (provider, base URL, model name, and a reference to its API key), the ego text, and the tool pins, one per tool, each opaque here as `agent.md` records. A secret appears only as a reference to where it is held.
- **A template is a named configuration and profile version, published in numbered versions and never modified.** Publishing a name again is its next version. Creating an agent copies the template version it names into revision 1 once; a later version of the template never reaches an agent created from an earlier one.
- **A profile is a named set of execution settings, published in numbered versions and never modified.** The settings are the workload's resources, its storage size and its storage class. A template or a definition naming an unpublished profile version is refused with `ErrNotFound`.
- **The container image is neither a profile nor a definition field.** It stays the operator's own configuration, for the security reason in ADR 0007.
- **A creation is identified by its organization and request id, and ends at most once.** Its actor is stored beside the key: the same key repeated by another actor is refused with `ErrRequestReused`, matching garam's refusal of a reused request id. It is stored `Pending` before garam is asked, then becomes `Registered` with the GRN garam minted or `Failed` with garam's refusal. A repeated request returns the stored outcome and never stores a second creation. One still `Pending` asks garam again, which `Registrar` requires to answer one key with one GRN. An error that is not a refusal leaves the outcome unknown, so the creation stays `Pending`. A repeated key naming a different template is refused with `ErrRequestReused`.
- **garam's registration is the `Registrar` interface, with no implementation yet.** Its wire contract is still being agreed (`garamsh/garam#1155` §2).
- **A configure request is recorded once, keyed by organization and request id, with its first outcome.**
  - **Stored.** The fields its authority bound are stored beside it as data: actor, operation, target, body digest, operation reference and the `{operator, epoch}` assignment.
  - **Outcome.** That is either the revision the request stored, or `Stale`.
  - **Repeat.** A repeat of the key returns that outcome, and any change to a bound field or the agent is refused with `ErrRequestReused`. Nothing here interprets the bound fields.
- **The store is `Repository`, and each rule's atomicity is the store's.** A configure request is inserted, or the stored one returned, in the same transaction as the revision it stores. A creation is inserted or the stored one returned in one step, and a registration stores the creation's outcome and revision 1 together. `internal/definition/repository/memory.go` holds it in process for tests and development.
- **The persistent store is a PostgreSQL database of the control service's own, through pgx v5.** `internal/definition/repository/postgres.go` implements `Repository`.
  - **Schema.** `schema.sql` beside it is embedded in the binary, which applies it at every start through `Postgres.ApplySchema`. Every statement in it only creates what is missing.
  - **Stale revision.** The key on `definitions` (agent, revision) refuses a second revision under one number. A configure request that loses that race is stored as `Stale` under a savepoint, so its request record still commits.
  - **Duplicate requests.** The keys on `creations` and `requests`, each (organization, request id), leave one record per request. A concurrent repeat waits for the first and then reads its outcome.
  - **Earlier databases.** The `creations` key moved from (actor, organization, request id) to (organization, request id) in issue #211, by editing `schema.sql` in place. No database had been deployed, so none is migrated. A database created before that keeps its old key, because `CREATE TABLE IF NOT EXISTS` alters nothing.
- **The console API is `internal/console`, a domain of its own over `internal/definition`'s surface** ([ADR 0039](adr/0039-serve-the-consoles-mutations-from-a-console-domain-over-the-definition-domains-surface.md)). Its one route so far is `POST /v1/orgs/{org}/agents/{agent}/revisions`, which configures an agent's definition. `{org}` is the organization's identifier, the last segment of its GRN, and `{agent}` is the agent's GRN.
  - **Body.** `{requestId, expectedRevision, profile: {name, version}, configuration: {model: {provider, baseUrl, name, apiKeyRef}, ego, tools}}`, at most 1 MiB, with unknown fields refused.
  - **Answer.** `200 {agent, revision}`, identical for every repeat of the request.
- **Its principal is the user garam's operation authority names.** The console presents the authority as `Authorization: Garam-Operation <authority>`, outside the body. The authority is never logged or stored. Every console mutation runs in this order, and stops at the first refusal:
  1. **Introspect.** The authority is introspected on garam's `POST /operation-authorities/introspection` under `Garam-Contract-Version: operation-authority.v1` (`garam@f2ac780`, `api/machine.yaml`). Introspection consumes nothing.
  2. **Bound fields.** Every field the answer binds is checked against the request: an expiry still in the future, the audience (this service), the organization, the operation (`agent:configure`), the target (the agent), and an assignment present.
  3. **Digest.** The SHA-256 of the exact body received, in lowercase hex, must be the digest the authority binds.
  4. **Request id.** The body is parsed, and its `requestId` must be the one the authority binds.
  5. **Record and apply.** Only then is the request recorded and the revision applied, so no stored outcome is revealed to a request whose authority fails.
- **garam's 500 and 503 are handled at the client** (`internal/console/introspector/garam.go`), as garam's ADR-0050 places a listener's 5xx. Because introspection consumes nothing, an attempt answered 500 or 503, or whose connection failed, is sent again: three attempts at most, waiting 200 ms and then 400 ms. After the third it is answered as undecided. An answer under another contract version is refused, not read.
- **Each refusal has one status**, chosen in `internal/console/respond.go` and nowhere else:

| Refusal | Status |
|---|---|
| No `Garam-Operation` authority presented | 401, with `WWW-Authenticate: Garam-Operation` |
| garam answers 404 (unknown, expired, another audience's), or the binding's expiry has passed | 401 |
| garam answers 403, a bound field differs from the request, or the body's digest differs | 403 |
| garam stays undecided (500 or 503 on every attempt, or unreachable) | 503 |
| The body is not one configure request | 400 |
| The agent has no revision, or the profile version is unpublished | 404 |
| A stale expected revision, or a request id reused with another binding, body or agent | 409 |

- **Domain behaviour is tested on the in-memory store**, in `internal/definition/*_test.go`, and the console's pipeline in `internal/console/*_test.go`, through `httptest` with a test double standing in for `Introspector`. `testing.md` keeps a real database out of the integration layer.
- **The e2e layer runs the built binary**, in `tests/control/`, against a PostgreSQL container that testcontainers-go starts. `make test-e2e-control` runs it, and `make test-e2e` runs it first.
  - **Runs today.**
    - The schema's tables exist, and each key refuses a second row under it, beside an accepted first.
    - The binary refuses to start without either API certificate file, and a plaintext request never reaches the route.
    - The configure route, called over HTTPS under a throwaway serving certificate the suite generates, refuses a request with no authority, and answers 503 while garam is unreachable.
  - **Skipped today.** Configures through the binary against a real garam are written but skip: concurrent configures on one revision storing one, and concurrent repeats of one request storing one record. garam has no supported way to mint an authority for a test organization (issue #230). Until it does, the PostgreSQL implementation of `Configure` and the store's races have no e2e coverage.

## Rationale

The console API, its order and its bindings are issue #211's, agreed with garam's PM against operation-authority.v1 (`garamsh/garam#1160`, merged at `fdfb76d`). That the console's mutations are a domain of their own, and that the request record stays with the revision, is [ADR 0039](adr/0039-serve-the-consoles-mutations-from-a-console-domain-over-the-definition-domains-surface.md).

The ownership — definitions, templates and revisions here, the GRN and authorization in garam — is `garamsh/garam#1155` D1 and D2, recorded in this repository as ADR 0032 (issue #205). The revision, copy-once and idempotent-creation rules are that design's console and creation contracts (§2) restated as what this service stores.

The store is [ADR 0036](adr/0036-persist-the-control-services-desired-state-in-its-own-postgresql-database.md): each rule above is one constraint or one transaction in PostgreSQL, and the owner's hosted services already run it.

A creation stays `Pending` on an unknown outcome rather than failing, because a timeout is not a refusal (`garamsh/garam#1155` §2, "timeout is unknown outcome"): failing it would let a retry under a new request id register a second agent for one intent.

## Open questions

- **What moves the e2e suite's PostgreSQL image.** It is pinned by digest in `tests/control/main_test.go`, which no Dependabot ecosystem reads, so it is moved by hand.
- **How the schema changes once a table holds rows.** `schema.sql` creates what is missing and alters nothing, so a change to an existing table needs a migration that no part of the binary performs yet.
- **What configuration the domain refuses.** It stores any configuration it is given, including an empty tool-pin set, which `agent.md` records `sherlock` refusing. The configure route now parses a request, and checks only that it is one configure request; which values to refuse there is not decided.
- **The rest of the API.** Create waits on `garamsh/garam#1167`, the controller routes on this repository's next slice, the runtime-status route on `garamsh/garam#1161`, and console reads and publishing on #220 (`garamsh/garam#1170`). Each is judged against `structure.md` §A new domain when it arrives (ADR 0039).
- **What activation re-reads.** The stored operation reference and `{operator, epoch}` snapshot are what a revision's first activation is to recheck through `GET /operation-references/{ref}`. Activation is not built.
- **Who may publish a profile or a template.** `garamsh/garam#1155` D4 makes editing a profile a high-trust action; nothing here checks an actor yet.
