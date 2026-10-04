# ADR 0035: Carry an agent's model and ego in its spec, and render them into the Pod through its type's descriptor

> Status: accepted
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #151: an agent this operator builds has no model. `sherlock` defaults `model.provider` to `mock` (`sherlock@07aa5c4:internal/config/config.go:36`), so a constructed agent runs, loads its tools, and answers nothing. Before this change the operator gave the agent's container one variable it chose and no setting anyone configured. The organisation's own statement, the ego, had no route either.

**Every claim about `sherlock` below was read at `07aa5c4`.**

- **The model is four settings in one config section.** `provider`, `base-url`, `model` and `api-key-env` under `model:` (`internal/config/config.go:124-133`). The key is never a setting. `api-key-env` names the environment variable that holds it.
- **The ego is reached by a flag and nothing else.** `--ego-file` is registered at `internal/config/config.go:165`, and `internal/config/ego.go:15` reads it with `fs.GetString`, the flag's own value. Neither the `SHERLOCK_` environment layer nor the config file reaches it.
- **The image's `CMD` is the subcommand.** `build/agent.Dockerfile` declares `ENTRYPOINT ["/sherlock"]` and `CMD ["agent"]`.
- **A spawned tool inherits the agent's whole environment.** `internal/tool/runner.go:50-51` sets `cmd.Env = os.Environ()` and says "Passing os.Environ() unmodified is the whole policy."
- **The model section has no `max-tokens` setting.** `ModelConfig` holds the four settings above and a timeout.

Issue #151 set three constraints before this decision:

- **Only the ego may be configurable, never the whole instruction set.** The agent composes its instructions as the ego plus its contract, and the reader and the writer of its store share that contract.
- **What the cluster can check lives here.** The account owner's boundary, reported from `garamsh/garam`, is that `garam` carries an alias and the cluster resolves it.
- **A model key's count follows the endpoints, not the organisations or the models.** The issue's comment of 2026-09-05 says so, and `garamsh/infra` was told the number on that basis.

The issue's body asked for a separate `Model` kind. The PM's decision of 2026-10-04 narrowed the issue to `sherlock` alone, put the settings on `AgentSpec`, and named the control store (ADR 0032, #205) as where the desired source moves later. This ADR implements that decision and builds no second source.

## Decision

**`AgentSpec` carries `model` and `ego`.** `model` holds `provider`, `baseURL`, `name` and `apiKeySecretRef`, and `apiKeySecretRef` names a Secret and a key in the `Agent`'s namespace. When `model` is set, every field is required. A field left out would fall back to the agent's own default for it (an OpenAI endpoint and model, for `sherlock`), and a key paired with another vendor's endpoint fails at the first request, not at admission. `ego` is free text. Both are optional, and unset leaves the agent on its image's defaults.

**The key is referenced and never carried, and it reaches only the agent's container, as a variable.** The container gets the variable from the Secret through `secretKeyRef`, and the config file names that variable under `model.api-key-env`. This operator reads the Secret's metadata to know it exists, and never reads the key. The credential road, a file copied into memory, does not reach the key: `sherlock` reads a model key from a variable only.

**A missing key Secret is handled like a missing credentials Secret.** The workload is not built, `Synced` is `False` with `ModelKeySecretMissing`, and the Secret's arrival wakes the `Agent` through the watch that already exists. Without this check, the Pod could not start while `Synced` reported `True`.

**The model's settings go into the config file ADR 0024 writes, and the ego goes into a second file beside it.** The same `config` init container writes both under the configuration directory, owner-only under the same umask, and each file's text travels in that container's environment and in no command. The agent is pointed at the ego file by its arguments, `agent --ego-file <path>`. The container's `command` stays unset, so the image's entrypoint still runs. The arguments replace the image's `CMD`, so they repeat its subcommand. The init container is built when an `Agent` declares a tool set, a model or an ego. One that declares none of the three builds the workload it built before, and one that stops declaring has every piece taken back out, the arguments included.

**Every name the agent reads goes through the descriptor (ADR 0029).** Three fields are added:

- the variable the key is placed in, `MODEL_API_KEY`, which sits outside the `SHERLOCK_` prefix so it binds to no setting
- the ego file's path under the configuration directory, `sherlock/ego.md`
- the function that turns the ego file's path into the container's arguments

The config file's model section is rendered by `sherlock`'s renderer, under `sherlock`'s setting names. A section the `Agent` does not declare is left out of the file. `sherlock` refuses an empty pin section, and a partial model section would inherit defaults.

**No `maxTokens` field.** `sherlock` has no such setting to deliver it to. The trap issue #151 named, a reasoning model whose tight budget yields success with empty content, therefore has no default here to sit on. A field this operator accepted and could not deliver would report success while nothing changed. That is the reason `agent.md` gives for reading no key for the required tool set.

## Consequences

Easier: an agent whose `Agent` names a model answers with it, and an organisation's ego reaches its instructions. Agents on one endpoint name one Secret, so the key count `garamsh/infra` was given holds as long as `apiKeySecretRef` stays the place a key is named.

- **Every tool the agent spawns can read the model key.** `sherlock` passes its whole environment to each tool it spawns, and the key is in that environment because `sherlock` reads it from no other place. The pin set stays out of the environment for this reason (ADR 0024), and that rule cannot be kept for a key `sherlock` reads only from a variable. The exposure is `sherlock`'s design and not this operator's choice. Narrowing it is a change to `sherlock`, either a key read from a file or a runner that strips the variable, and is recorded as an open question in `agent.md`.
- **`agent.md`'s rule that key material does not travel through environment variables is now scoped to the credential.** That rule was written for the agent's `garam` credential, which `garam`'s reader takes as a file. It still holds for that credential. A model key is the other kind: its reader accepts only a variable.
- **The image's `CMD` is restated here wherever an ego is declared.** If `sherlock` moves its subcommand, an `Agent` with an ego runs the old one until the descriptor is changed. An `Agent` without an ego is unaffected. ADR 0024 refused the `--config` flag because an XDG road existed that left `CMD` to the image. No such road exists for the ego.
- **An agent this operator constructs still has no model.** Construction writes neither field, and the PM's decision puts the desired source in the control store (ADR 0032) rather than in a second path here. Until then, a model reaches a constructed agent only by editing its `Agent`, and the operator overwrites nothing but `spec.image` (ADR 0018).
- **The model and the ego are visible in the Pod's spec, and the key is not.** The Pod carries the Secret's name and key, and the ego's text sits in the init container's environment. An ego is the organisation's statement to its own agent and is no secret, and the `Agent`'s spec already holds it.
- **Nothing here has been run against a real `sherlock`.** The integration layer verifies the workload this operator builds, a Pod carrying it is admitted under PodSecurity `restricted`, and the writing shell is run against the rendered text. envtest has no kubelet, and the e2e suite runs a substitute image that reads neither file. Whether an agent then answers through MiniMax is a claim only a bring-up can settle.

Ruled out:

- **A separate `Model` kind.** The issue's body asked for one. The PM's decision of 2026-10-04 put the settings on `AgentSpec`, and the desired source is moving to the control store. A kind added now would be a second source, which that decision forbids.
- **A key per model.** It would make the key count the model count, which issue #151's comment of 2026-09-05 refused before it was built.
- **A configurable instruction set.** Issue #151's constraint rules it out. `ego` is the only text the field reaches.
- **The key as a file.** `sherlock` reads none.
- **The ego through the config file or the environment.** `sherlock` reads neither for it.
- **Restating the image's `ENTRYPOINT` as `command`.** Arguments alone reach the flag, and the entrypoint stays the image's.

Nothing is superseded. ADR 0024 is extended with a second key family and a second file, ADR 0029 with three fields, and ADR 0009's refusal of a ConfigMap is not reopened, because nothing here adds an object.
