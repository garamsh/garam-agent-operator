# ADR 0060: Give every placed adapter its agent's outbox on either source, and keep the control settings to the Control source

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #310, found in the `garamsh/garam#1196` run 6 acceptance (scenario 8, order 1): a `Garam`-source (legacy) agent's replies never left its Pod. `message_send` on channel `garam` writes each reply into `sherlock`'s outbox on the state claim, `memory/outbox/`. The adapter beside it was never given that directory, so it forwarded nothing. garam marked the message `completed`, the sender received nothing, and the replies piled up on the volume.

**Why it was gated.** [ADR 0049](0049-give-a-managed-agents-adapter-the-control-services-settings-behind-a-switch.md) gave the outbox only to a fenced adapter. It cited `garam@e81a1e0`'s adapter, where `OUTBOX_DIR` was "required beside the control settings", and its "Which agents" line kept every `Garam`-source agent's adapter legacy, with no outbox. No release of this operator has given a legacy adapter its outbox since the adapter was first placed (ADR 0034).

**What the pinned adapter accepts.** At this repository's pinned `GARAM_REVISION`, `garamsh/garam@59fe68dca4f5f0b42e6f159eba324db0c65486d9`:
- `internal/cli/delivery.go:98-118`, `fenced()`. With none of the three control settings set, the mode is legacy and `OUTBOX_DIR` is not checked. The refusal "required beside the control settings" (`:112-113`) applies only when all three are set. A `GARAM_ADAPTER_CONTROL_SOCKET` without them is refused (`:107-108`).
- `internal/cli/delivery.go:121-125`, `adapterLoop`: "the outbox is forwarded from wherever it is named".
- `internal/cli/cli.go:91-94`: `GARAM_ADAPTER_OUTBOX_DIR` is "required beside the control settings; otherwise optional, and nothing is forwarded without it".

These are the lines garam's PM quoted on #310 at `f54b9e8`. `git diff 59fe68d f54b9e8 -- internal/cli/delivery.go internal/cli/cli.go` is empty.

## Decision

**Every agent with an adapter placed gets its outbox, on either source.** ADR 0049's "Which agents" line is amended for the outbox alone:
- **The `outbox` init container.** It makes `memory/outbox` on the state claim as the Pod's user, at `0770`, where the directory is absent, before the adapter starts.
- **The mount.** The state claim at `subPath` `memory/outbox`, read-write, at `/run/garam/outbox`, in the adapter alone.
- **The setting.** `GARAM_ADAPTER_OUTBOX_DIR=/run/garam/outbox`.

**The control settings stay where ADR 0049 put them:** a `Control`-source agent, with `--agent-adapter-control` on.
- **Which settings.** `GARAM_ADAPTER_CONTROL_URL`, `GARAM_ADAPTER_CONTROL_ROOT_FILE`, `GARAM_ADAPTER_PLACEMENT_TOKEN_FILE` and the control root's volume.
- **What a legacy adapter keeps.** `GARAM_ADAPTER_GATEWAY_AGENT`, as before.
- **What it never gets.** `GARAM_ADAPTER_CONTROL_SOCKET`, which garam refuses without the control settings.

**A fenced adapter is unchanged.** Its settings and mounts are what they were, in the same order, so its Pod template does not change.

## Consequences

- **A legacy agent's replies are forwarded.** That covers every agent on the `Garam` source until it is cut over (`garamsh/garam#1171`), and a `Control`-source agent while the flag is off.
- **Each such agent's Pod rolls once,** because the StatefulSet's template gains the init container, the adapter's mount and its setting.
  - **The roll.** It is an ordinary template change, as bringing `spec.image` current is (ADR 0018). The StatefulSet deletes the old Pod, and the writer fence (ADR 0042) holds it, through the `agent.garam.sh/writer-stopped` finalizer on the template, until its writers are seen to stop. Only then does the replacement start on the claim.
  - **What is lost.** A session the agent held is lost, as with any roll.
  - **What is kept.** Replies already in `memory/outbox` stay there. The init container leaves an existing directory alone, and the adapter forwards them once it starts.
- **The adapter image.** A legacy adapter given `OUTBOX_DIR` must be one that reads it in legacy mode: `garam@59fe68d` does. An adapter older than ADR-0084's settings ignores a variable it does not read.

Ruled out:
- **Giving a legacy adapter the control settings too.** It would turn the legacy mode into the fenced one, which a `Garam`-source agent cannot activate in: it registers no placement (ADR 0049).
- **Documenting the gap instead.** garam's adapter forwards a legacy outbox, so the only gap was the directory this operator withheld.
