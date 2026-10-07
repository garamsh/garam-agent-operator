# ADR 0064: Give an agent Pod the grace period one full step of its drain needs

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md) releases a deleted agent Pod only once its writers report `terminated`, and it left `terminationGracePeriodSeconds` unset. The Kubernetes default of 30 seconds therefore applied, and nobody had chosen it. Issue #282 asked for a measured value with its derivation beside it.

**What sherlock's drain does**, at `sherlock@b3c05c2`, the commit `v0.2.0` tags:
- **SIGTERM starts it** (`cmd/sherlock/agent.go:263`).
- **The turn in flight runs to completion, without a bound of its own.** Admission stops, and no queued turn starts (`internal/gateway/queue.go:211-224`, `internal/gateway/runner.go:22-60`).
- **Then the store closes.** The HTTP server shuts down within its 10-second `shutdownGrace`, and the queue closes the memory store, releasing the writer lock last (`internal/gateway/httpserver.go:14,38-57`, `internal/memory/sqlite/sqlite.go:143-153`).

**What bounds one step of a turn.** A turn is up to 16 iterations (`internal/agent/service.go:14`), each a model request and then the tool calls it asks for:
- **The model request** has sherlock's `model.timeout`, 60 seconds by default (`internal/config/config.go:43`).
- **A workspace command** has the workspace's `exec-timeout`, 30 seconds by default (`internal/config/workspace.go:22`). `shell_exec` allows the workspace 5 seconds beyond it (`tools/shell_exec/shell_exec.go:35-39`).
- **This operator renders neither setting**, so both defaults apply.

**What was measured on Kind (#282).** The image was `sherlock-agent:v0.2.0` at manifest `sha256:793290c87edf1042fe414a6660e2c541b3ed9c81c23ef04f47232a829d56c145`, the one garam#1157's joint runs pin. Its model and embeddings were a fake endpoint that answers a chat after a set delay with the `memory_write` call that ends the turn. SIGTERM was a Pod delete with an explicit grace. The time is the kubelet's `finishedAt` less the delete, to the second.

| Case | Runs | Exit |
|---|---|---|
| Idle | 3 | code 0, within the second of SIGTERM |
| Idle after one committed turn | 2 | code 0, within the second |
| Mid-turn, the model answering 20 s after the request, SIGTERM about 2 s in | 2 | code 0 at 18 s, the model's remaining wait |
| Mid-turn, the model answering at 45 s, grace 30 s | 1 | code 137 at 30 s: SIGKILL, and the turn is lost |

A whole turn with a model that answers at once took 13 ms from enqueue to commit. So the commit and the close cost milliseconds, and what the drain waits for is the step in flight.

## Decision

**Every agent Pod is given `terminationGracePeriodSeconds: 100`, one full step of a turn**:
- **60 s:** a model request at sherlock's default timeout;
- **35 s:** a workspace command at its default ceiling, with the tool's dispatch margin;
- **5 s:** the commit and the store's close, measured under a second, and the adapter sidecar's teardown, which the kubelet starts once the agent and the workspace have exited, inside the same grace period.

**The value is a sum of named constants on sherlock's type descriptor** (`internal/controller/agent_types.go`), each citing the sherlock line it comes from, and the renderer writes the descriptor's value.

**The derivation holds only while this operator renders neither default.** A test fails if the workload starts rendering a model timeout or an exec ceiling, so the change that renders one also has to change the sum.

**A turn longer than one step can still be cut at 100 seconds.** This is a known, accepted limit. sherlock bounds a turn only at 16 iterations, about 25 minutes of steps, and a grace period that long would hold every replacement Pod for as long, since the writer fence waits for the writers to stop.

## Consequences

- **A normal stop commits the step in flight.** The fence then reads exit code 0, where under the 30-second default any model request still outstanding at about 29 seconds was cut by SIGKILL.
- **A stop can take up to 100 seconds before the replacement Pod.** The fence holds the next Pod until the writers stop. That applies to a delete, a suspend, an image change and every other template roll.
- **A turn cut at 100 seconds is lost, not corrupted.** SQLite's write-ahead log rolls back the uncommitted transaction, and the turn's messages stay uncommitted. The fence still releases the Pod: a SIGKILLed writer is `terminated`, with exit code 137 in its evidence.
- **Changing sherlock's defaults, or rendering either setting here, changes this value.** The constants cite `sherlock@b3c05c2`, and a newer image is checked against them when this operator moves to it.

Ruled out:
- **30 seconds, set explicitly.** It covers an idle agent and the commit path, but it cuts any model call still outstanding at about 29 seconds, and a cut turn loses the agent's work.
- **A grace period covering a worst-case turn.** At about 25 minutes, it would hold every replacement Pod for as long.
- **A bare number.** Without the sum beside it, nothing shows which default it rests on, and nothing has to change when one moves.
