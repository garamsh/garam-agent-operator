# ADR 0049: Give a managed agent's adapter the control service's settings and its outbox, behind a switch until activation is served

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

#218. `garam` merged the adapter's side of agent-execution.v1 (`garam`'s ADR-0084) at `garamsh/garam@e81a1e0`. At that commit the adapter reads ten settings (`internal/cli/cli.go:64-91,182-191`):

- `GARAM_ADAPTER_AGENT`, `MACHINE_URL`, `GATEWAY_URL`, `TLS_CERT_FILE`, `TLS_KEY_FILE` and `SERVER_ROOT_FILE`, as before;
- `CONTROL_URL`, `CONTROL_ROOT_FILE` and `PLACEMENT_TOKEN_FILE`, which select its fenced mode. They are taken all together or none at all, and anything between is refused (`internal/cli/delivery.go:94-109`). With none, it runs in the legacy, unfenced mode;
- `OUTBOX_DIR`, required beside the control settings (`delivery.go:102-103`). The adapter forwards the entries it finds there and removes those `garam` acknowledged (`internal/delivery/outbox.go:142-157`).

`GARAM_ADAPTER_GATEWAY_AGENT` is no longer read. The adapter this operator was written against, `garam@fdfb76d`, refuses to start without it (`internal/cli/delivery.go:90-91`).

**Why fenced mode cannot be the default yet.**

- Activation goes through the control service's `POST /v1/agents/{grn}/activations`, which is built on a stacked branch that is not merged.
- Only a placement the manager registered can be activated (#259), and only agents on the `Control` source register one.

**Where the outbox is.** `sherlock` derives it as `outbox/` beside the memory store (`sherlock@44aaa55:internal/gateway/outbox.go:18-26`), on the state claim. It creates it with `os.MkdirAll(dir, 0o755)`, which leaves an existing directory alone (`tools/message_send/message_send.go:108`). Clearing a forwarded entry is the consumer's job (`sherlock@44aaa55:docs/architecture/deployment.md` §Volumes and ownership).

[ADR 0044](0044-give-an-agents-state-and-its-workspace-separate-claims.md) reserved the state claim for the agent, and for the adapter's outbox access alone.

## Decision

**A manager flag, `--agent-adapter-control`, off by default, gives the adapter the control service's settings.**

- **What it needs.** It requires `--control-address`, and the manager refuses to start without it.
- **Which agents.** It applies only to agents on the `Control` source with the adapter placed. A `Garam`-source agent registers no placement and stays legacy, whatever the flag says.

**Off, and for every `Garam`-source agent, the adapter gets today's seven settings, `GARAM_ADAPTER_GATEWAY_AGENT` included.**

**On, a managed agent's adapter gets the ten settings, without `GARAM_ADAPTER_GATEWAY_AGENT`:**

- `GARAM_ADAPTER_CONTROL_URL` is `https://` and `--control-address`.
- `GARAM_ADAPTER_CONTROL_ROOT_FILE` is `/run/garam/control/root.pem`.
- `GARAM_ADAPTER_PLACEMENT_TOKEN_FILE` is `/run/garam/placement/token`, [ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md)'s copy.
- `GARAM_ADAPTER_OUTBOX_DIR` is `/run/garam/outbox`.

**The compatibility rule between `--agent-adapter-image` and this operator:**

- With the flag off, any adapter image from `garam@fdfb76d` through `e81a1e0` runs, because `GATEWAY_AGENT` is still set and an image that does not read it ignores it.
- With the flag on, the adapter image must be `garam@e81a1e0` or later. An older one requires `GATEWAY_AGENT` and reads no control setting.

**The adapter gets the outbox, and nothing else of the state claim.**

- **The mount.** It mounts the state claim read-write with `subPath` set to the outbox's path (`memory/outbox` for `sherlock`), at `/run/garam/outbox`. It writes because it clears acknowledged entries. It never sees the memory store.
- **The directory is made before the adapter starts.** A `subPath` the kubelet creates is root-owned, and the agent could not write it. So a run-to-completion init container, `outbox`, runs before the adapter: ADR 0034 orders the native sidecar after those.
  - It runs the copy image as the Pod's user, with the state claim mounted.
  - It creates `memory/outbox` at mode `0770` where the directory is absent, and its parent where that is absent.
  - It touches nothing else. An existing outbox keeps its mode and its entries.
- **Who else mounts it.** The workspace never mounts the state claim.

**The control root takes the config file's road.**

- **Where it comes from.** It is public, and the manager already reads it from `--control-trust-file` for its own calls. The reconciler reads that file at each pass.
- **How it reaches the adapter.** The existing config writer writes it, from a variable set on that init container alone, into a dedicated `emptyDir`, `control-root`. That volume is mounted read-only into the adapter only.
- **What this adds and costs.** It needs no new object kind and no new RBAC. A rotated root changes the Pod template, so each managed agent's Pod restarts once, which is acceptable for a rare event.

**The flag flips** once the control service serves activation on `dev` and the deployed `--agent-adapter-image` is `garam@e81a1e0` or later. That change makes it the default, and drops `GATEWAY_AGENT` for managed agents everywhere.

## Consequences

- **The adapter can be fenced** without a second release of this operator, as soon as both conditions hold. Until then, every adapter keeps running exactly as before.
- **The outbox is collected.** A managed agent's replies reach `garam` through the outbox, and the adapter clears what was acknowledged. With the flag off, nothing is forwarded from the outbox, as before.
- **Turning the flag on rolls every managed agent's Pod once,** because the adapter's settings and mounts, the outbox init container and the control root all change. Each Pod goes through ADR 0042's fence, and a new placement is registered for it.
- **A wrong adapter image with the flag on refuses to start,** visibly on the Pod. That is the reason the rule above is stated.
- **This settles `agent.md`'s open question about how the adapter reaches the outbox.**
