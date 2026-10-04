# ADR 0032: Own an agent's desired definition in this project's control service, and render every Agent carrying a GRN from it

> Status: accepted
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The owner accepted a separation of services on `garamsh/garam#1155` (decisions D1 to D9, accepted 2026-10-04), executed under the umbrella `garamsh/garam#1157`; issue #205 is this repository's part of it. Its ownership table divides what each service holds:

| Owner | What it holds |
|---|---|
| `garam` | Organizations, membership, authorization and delegation, an agent's stable identity and display name, assignment, collaboration |
| This project's control service | Execution definitions and templates, desired revisions, allowed deployment profiles, which host and storage a GRN is bound to, reconciliation status |
| The manager in a customer cluster | The `Agent` objects it renders, and the workloads it reconciles from them |

Three decisions recorded here put the first row's neighbour in `garam` instead:

- [ADR 0007](0007-claim-definitions-from-a-poller.md) divides a definition, which says what an agent is and which `garam` holds as `values`, from this operator, which builds it. Composition is `garam`'s there.
- [ADR 0012](0012-declare-an-agents-tool-set-in-its-definitions-values.md) declares an agent's tool set as the key family `tools.pins.<name>` in that definition's `values`.
- [ADR 0031](0031-support-agents-declared-in-garam-on-one-cluster-of-the-sherlock-type.md) §1 states the supported scope as agents declared in `garam`.

What `garam#1155` gives as the reason for moving them (D1, D2): `garam` holding desired runtime values makes it the owner of execution delivery and retry state, two writable stores would need conflict resolution, and `Agent` objects as the managed authority would need either a console inside the customer cluster or a command channel into it.

ADR 0031 rejected declaring `Agent` objects in the cluster for `garam` to observe, because identity, per-agent certificate issuance and single ownership would leave `garam`. This decision moves none of the three: the GRN, the certificate route and the claim's epoch stay `garam`'s, and an `Agent` stays something rendered, not a source.

The control service is not built, and the wire contracts between it, `garam` and the manager are being agreed with `garam`'s PM separately. This record states ownership and invariants; it states no wire shape, no store technology, and no mechanism the code does not have.

## Decision

**An agent's desired definition is owned by this project's control service.** Execution definitions, templates, desired configuration and the revisions of each are held in the control service's store. `garam` owns the agent's stable identity (its GRN), organization authorization and collaboration. The manager in the customer cluster renders each `Agent` from the control store.

The rules that hold under that split:

1. **One writable desired source.** The control service's store is the only place an agent's desired definition is written. For every `Agent` carrying a GRN, the manager renders the spec from that store and owns those fields, so a hand edit to one is reverted. An `Agent` with no GRN — one a person wrote — stays the development path ADR 0031 §4 names, and is not rendered from anything.
2. **Names are preserved.** The `Agent`, its StatefulSet and its volume claim stay named for the digest of the GRN, as `internal/garam/constructor/` names them. Moving where a definition is held re-creates no workload and detaches no volume, so the memory store on it (`SHERLOCK_MEMORY_PATH`, issue #157) survives the move.
3. **Identity is unchanged.** No new GRN is minted for an agent that already has one, and the assignment epoch stays `garam`'s, as [ADR 0016](0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md) carries it.
4. **Connections are outbound.** Every connection leaves the customer cluster. No broker stands between the manager and the control service, and `garam` is not a relay between them.
5. **The container image stays operator configuration.** No field of a desired definition a member edits names the image. ADR 0007's reason stands unchanged: a member choosing an agent's container is choosing what this operator runs as a root-capable workload in a cluster they cannot see.

What this supersedes, part by part:

| ADR | Superseded | Stands |
|---|---|---|
| 0007 | That composition is `garam`'s, held as a definition's `values`, and the division resting on `garam`'s ADR-0026 | The claim and the epoch it carries, the poll as a `Runnable`, the three credential flags, the certificate read at each handshake, the GRN in `AgentStatus`, the terminal conflict, an unset address making no call, and the image as operator configuration with its security reason |
| 0012 | That the tool set is declared in a `garam` definition's `values`: the key family moves to the control store's desired definition | The pin opaque to this operator, `tools-dir` as construction, the declaration reaching the Pod as a file and never as an environment variable, no key carrying a credential, the `Agent`'s spec as what carries the declaration, and the required set unreachable from configuration |
| 0031 | §1, "agents are declared in `garam`" | §2 one cluster, §3 `sherlock` only, §4 a hand-written `Agent` for development and bring-up |

What is built today does not change with this record. The poller still reads definitions from `garam`, claims them and constructs `Agent`s from their `values`, and that stays the present mechanism until the change moving the manager onto the control store lands; only `spec.image` is kept current after construction ([ADR 0018](0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md)).

## Consequences

Easier: a change to an agent's desired definition has one place to be written and one owner of its schema and validation, and a pin set edited after construction has a path to the running agent — rule 1 makes the manager the writer of every rendered field, not of `spec.image` alone.

Harder: until the control service and the manager's pull from it are built, the responsibility documents describe two things at once — the ownership decided here and the mechanism that still reads `garam` — and each statement about the second is marked as the present mechanism. A migration has to import every existing definition keyed by its GRN without changing a name, which rule 2 makes possible and does not perform.

Ruled out: a second writable desired source, in `garam` or in the cluster, which would need conflict resolution between them; minting a new identity for an agent on migration; a connection into the customer cluster or through `garam`; and a desired-definition field a member edits that names the image.

Not decided here: the wire contracts, the control store's technology, the second image the control service is delivered as, and how a migrated definition is cut over. Each is its own issue.
