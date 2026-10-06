# ADR 0053: Add the embedding and the workspace size to the C2 wire, and refuse at the control service a model its agent could not start with

> Status: accepted
> Date: 2026-10-06

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0040](0040-release-desired-state-to-controllers-from-a-distribution-domain-each-decision-proved-by-garam.md) §6 pins the C2 wire: the desired feed's `profile` and `configuration`. Two issues add to it, and the PM batched them into one change so that the wire is amended once:

- **#266, the control half.** [ADR 0052](0052-carry-an-agents-embeddings-endpoint-beside-its-model-and-refuse-changing-it-once-set.md) (PR #278) defines `configuration.model.embedding {baseUrl, name, apiKeyRef}`, which the manager parses. It leaves two decisions to the control service:
  - how configure and create refuse a model other than the mock that names no embedding;
  - how configure refuses a change to an embedding once set.
- **#250.** [ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md) gives the workspace a claim of its own, sized by `spec.workspaceStorageSize`. A managed agent's profile has no way to set it.

The PM settled the five points this record states (1 to 5) on this change's dispatch, against the manager's side in #278.

## Decision

**The C2 wire gains two optional fields.** Each is absent, not empty or null, where the revision names none:
- `configuration.model.embedding` is `{baseUrl, name, apiKeyRef}`, exactly as ADR 0052 defines it. `apiKeyRef` is `<secret-name>/<key>`, or empty for an endpoint that takes no key.
- `profile.workspaceStorageSize` is a quantity string, as `storageSize` is.

**The control service's refusals.** They are all made before anything is stored or garam is asked, so a corrected request may follow under the same request id.

1. **A model its agent could not start with is `400 {"kind": "embedding_required"}`.** This covers a model whose provider is not `mock` and that names no embedding, and an embedding missing its base URL or name. A malformed embedding key reference is `400 invalid_api_key_ref`, the model's own kind. An empty one is accepted as no key.
2. **One check makes the refusal everywhere a configuration enters.** It runs at template publication, at configure, and at create, which re-checks the template it copies.
3. **Once set, the embedding is not changed or removed.** A configure based on the agent's latest revision is refused `409 {"kind": "embedding_immutable"}` when:
   - that revision names an embedding;
   - and the request's embedding is absent, which covers a move to the mock, or differs from it in base URL or name.

   Other cases:
   - **Key only.** A change to the embedding's key alone is accepted.
   - **Stale first.** A request based on an earlier revision is refused as stale first.
   - **Another organization.** A request in another organization is refused as not found, so neither refusal discloses that organization's agent.
   - **No embedding before.** A latest revision naming no embedding, such as a cutover import's or a mock's, constrains nothing.
4. **The mock may name an embedding.** It is accepted and carried, as the manager accepts it.

**The workspace size.**

5. A profile's settings gain an optional workspace size.
   - **The manager.** It parses the size as it parses `storageSize`. One that does not parse, or is not above zero, leaves the revision unrendered with `ErrMalformed`. Otherwise it writes the size into `spec.workspaceStorageSize` on every render. A profile naming none clears the field, so the workspace claim follows `spec.storageSize`, as ADR 0044 defines.
   - **A size the claim cannot follow.** It is reported as `StorageSizeImmutable` by the path ADR 0044 already gives the workspace claim, and never forced.

## Consequences

- **Compatibility.** Both fields are additions. The manager's client decodes the feed without refusing unknown fields, so a manager built before #278 ignores the embedding. A manager built before this change ignores the workspace size, and its claims stay sized at `storageSize`.
- **Configuring a model needs its embedding.** A console request naming a model other than the mock must name its embedding too. Agents with no embedding are configured by the mock, or by naming no model at all.
- **An embedding is permanent through the console.** Re-embedding an agent's memory is not an operation this service offers, as ADR 0052 records for the `Agent`.
- **Ruled out.**
  - Sending an empty embedding or workspace size where none is named, which the manager would read as malformed.
  - Refusing only an in-place change to the embedding, which two configures would walk around.
