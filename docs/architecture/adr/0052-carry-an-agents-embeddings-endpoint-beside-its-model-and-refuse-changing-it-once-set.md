# ADR 0052: Carry an agent's embeddings endpoint beside its model, and refuse changing it once set

> Status: accepted
> Date: 2026-10-06

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #266, found in Garam's joint kind run (garamsh/garam#1196): an agent rendered for `sherlock` v0.2.0 cannot start with any real model. Every fact below is from `sherlock@b3c05c24bc2a346521251556935bfe0dc70f8cd1` (`v0.2.0`):
- **The section.** `sherlock` reads an embeddings endpoint from its `embedding` section: `base-url`, `model` and `api-key-env` (`internal/config/config.go:150-157`).
- **When it is required.** With any `model.provider` but `mock`, a missing `base-url` or `model` exits the agent at startup (`cmd/sherlock/model_provider.go:73-88`; `docs/architecture/deployment.md:177`). An empty `api-key-env` sends no key; one naming an unset variable fails.
- **What recall needs.** Recall embeds the task text on every turn (`deployment.md:115`).
- **Why the model cannot change.** Stored episodes carry the vectors the endpoint produced, and recall refuses a query whose dimensions differ from theirs: "the embedding model changed" (`internal/memory/sqlite/conversation.go:178`).

Nothing here carried an embedding: not `AgentSpec.Model` (ADR 0035), not the control service's configuration (ADR 0032), and not the C2 wire (ADR 0040).

The PM settled the source and the refusals on this dispatch, recorded on #266.

## Decision

**Per agent, beside the model.** An agent's embeddings endpoint is part of its model's desired state, as the model is: in `AgentSpec`, in the control service's definition, and on the wire between them.
- **Not the profile.** A profile is shared, so one edit would change the endpoint, and break recall, for every agent naming it.
- **Not the operator's flags.** Flags are deployment-wide and outside the control service, which owns desired state (ADR 0032).

**The `Agent`.** `spec.model.embedding` is `{baseURL, name, apiKeySecretRef?}`.
- **The key.** `apiKeySecretRef` is optional: a local endpoint takes no key. Where it is set, the Secret is waited for as the model's is, under `EmbeddingKeySecretMissing`. The key reaches the agent's container alone as `EMBEDDING_API_KEY`, and the config file names that variable under `embedding.api-key-env`.
- **The rendering.** The renderer writes `sherlock`'s `embedding` section from it.

**Refused at admission.** A model whose provider is not `mock` and that names no embedding is refused, so a broken model never renders into a crash.
- **On the `Agent`:** by a CEL rule on `ModelSpec`.
- **At the control service's configure and create:** the same rule, and its `kind`, are the control half's to bind.

**Refused once set: change and removal.** Once `spec.model.embedding` is set, `spec.model` and its embedding stay, with `name` and `baseURL` unchanged. `apiKeySecretRef` stays changeable.
- **Why removal is refused too.** A rule that allowed removal would be walked around in two updates, removing the embedding and then adding another. The stored vectors would break the same way.
- **The cost.** An agent with stored vectors cannot quietly drop its model. Re-embedding or retiring such an agent is a separate, explicit operation, not a spec edit, and no such operation exists yet.
- **On the `Agent`:** one CEL transition rule on `AgentSpec`.
- **At the control service's configure:** `409 embedding_immutable`, for change and removal both, in the control half.
- **What the manager's renderer does.** It applies a revision only through the API server, so a revision that would change or drop the embedding is refused there and leaves the `Agent` as it was.

**The C2 wire gains `configuration.model.embedding`**, an addition to ADR 0040's wire. It is `{baseUrl, name, apiKeyRef}`, camelCase as the rest of the wire:
- `apiKeyRef` is `<secret-name>/<key>` as the model's is (ADR 0043), or empty for no key.
- The object is absent where the revision names none.
- **What the manager refuses.** It refuses, as malformed, a model other than `mock` with no embedding, an embedding missing its base URL or name, or a malformed key reference.
- **What is not yet sent.** The control service's side (storing, validating and sending the field) is control code on the held stack, dispatched against this definition. Until it lands, a managed agent's revision names no embedding, and so can configure only the `mock` model.

**Compatibility with the image the lab pins.** `sherlock@837b80afe2729230daeab21621f3f97cabb6dfb4` predates the `embedding` section, on a line beside `v0.2.0`. Its loader warns on an unknown key and does not fail (`reportUnknownKeys`, `internal/config/config.go:321-337`), so an `Agent` rendered with an `embedding` section starts there, logging "unknown config key: nothing reads it" for each of its keys.

## Consequences

- **An existing `Agent` with a non-mock model and no embedding** cannot be written with that model again: an update that changes its model must add an embedding. Whether an update elsewhere in its spec is re-checked against the unchanged model is the API server's validation ratcheting, and no test here measures it. Such an agent could not start on `v0.2.0` either.
- **An `Agent` that once had an embedding keeps a model for as long as it exists.**
- **Changing an agent's embedding model needs a new agent, or the re-embedding operation nothing provides yet.**

Ruled out:
- **Carrying the endpoint in the profile or in the operator's flags** (above).
- **Applying a change to the embedding model.** It would silently break recall over everything the agent remembers.
- **Refusing only an in-place change.** The two-step path stays open.
- **Making the embeddings endpoint optional in `sherlock`.** That is `garamsh/sherlock`'s change, and recall would then have nothing to embed with.
