# ADR 0040: Release desired state to controllers from a distribution domain, each decision proved by garam, over a position-ordered feed

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #211's second slice serves the routes the manager calls: the desired feed it pulls (issue #217, part 2) and the status it reports, the contract C2 that issue names. [ADR 0039](0039-serve-the-consoles-mutations-from-a-console-domain-over-the-definition-domains-surface.md) §Decision 4 requires judging these routes on `structure.md` §A new domain, rather than folding them into `internal/console` by default.

Their principal is not the console's. A controller is the operator its leaf certificate names. garam proves it per decision through `introspectController`: `POST /operators/{controller}/introspection` under `operation-authority.v1` (`garam@f2ac780`, `api/machine.yaml`; read at `garam@6cfdde2`). The request carries the leaf exactly as presented and, optionally, an agent. The answer is 200 only when every check holds, and with an agent it carries that assignment's epoch. Every non-200 refuses. The contract requires a fresh agent-bound proof before each release of an agent's content, each status write and each placement, and again after a long wait.

What the store did not hold before this slice:

- **Which controller a revision is for.** The authority behind each configure binds `{operator, epoch}`, but slice 1 kept it only on the request record.
- **An order the feed can resume from.** Revisions were ordered per agent and not across agents.
- **What a controller reported.** Nothing stored status.

## Decision

1. **The controller routes are their own domain, `internal/distribution`.** It owns the proof port `Prover`, its implementation in `internal/distribution/prover/`, and the routes. The test in `structure.md` §A new domain holds on each condition:
   - **Vocabulary and lifecycle.** Controller, leaf, proof, epoch, feed, cursor and status are none of them the console's.
   - **Dependent outside.** The manager depends on it.
   - **Stable surface.** Its surface is the C2 wire below.
   It imports `internal/definition`'s root package only, and neither `internal/definition` nor `internal/console` imports it.
2. **Every revision records the assignment it was authorized for, and only a revision recorded for a controller is released to it.**
   - **Configure.** It records the `{operator, epoch}` its authority bound.
   - **Creation.** It will record the `{grn, epoch}` garam's managed create answers (`garamsh/garam#1167`). Until then revision 1 records none, and is released to no controller.
   - **Release.** An agent is released when its **latest** revision is recorded for the calling controller and garam's agent-bound proof, obtained for that answer, returns that revision's recorded epoch.
3. **The feed is ordered by a single position**, taken in the same transaction as every stored revision. Every writer takes the next position from one row, so positions follow commit order, and a reader holding a position has seen every revision below it. The cursor is that position.
   - **Withheld revisions.** One the agent-bound proof refuses (403, 404, 422) or proves under another epoch is skipped: the cursor moves past it, and it is not sent again unless a newer revision is stored.
   - **Undecided proofs.** If garam leaves any proof undecided, the whole answer is 503 and the cursor does not move.
4. **Status is monotonic per field and stored in `internal/definition`, beside the revisions it names.** A lower report is answered with what is stored, not refused. `appliedRevision` is set only by the runtime's own report, a later slice.
5. **What two domains both need of garam sits below them.** That is `internal/garammachine`: the machine-listener client with its contract header and bounded 500/503 retry, shared by the console's introspector and the prover. Likewise `internal/certificate`, the serving certificate read again when its files change, which the composition site wires.
6. **The C2 wire, pinned here.** Every property is camelCase, as garam's machine JSON is. Both routes need the client certificate a controller presents in its TLS handshake on the API listener.
   - `GET /v1/operators/self/desired?after=<cursor>&waitSeconds=<0..30>`. An absent `after` starts from the beginning.
     - **Answer.** `200 {"cursor": "<decimal>", "agents": [{"agent", "revision", "epoch", "profile": {"name", "version", "resources", "storageSize", "storageClassName"}, "configuration": {"model": {"provider", "baseUrl", "name", "apiKeyRef"}, "ego", "tools"}}]}`.
     - **Long poll.** With nothing new, the request waits up to `waitSeconds` and answers an empty `agents` under the same cursor.
     - **Refused query.** A malformed cursor, or one ahead of the feed, is 400, and so is a `waitSeconds` outside 0–30.
   - `POST /v1/operators/self/agents/{agent}/status`, body `{"observedRevision", "renderedRevision"}`, each from 1 to the agent's latest revision.
     - **Answer.** `200 {"agent", "observedRevision", "renderedRevision", "appliedRevision": null}`, as stored.
     - **Refused report.** An agent whose latest revision is recorded for another controller, or whose proof fails or names another epoch, is 403.
   - **Statuses on both routes.** No client certificate, or one naming no single GRN, is 401. A session proof refused, or naming another operator than the certificate, is 403. Undecided is 503.

## Consequences

- The manager's pull (#217) has a wire to bind to, and a restart resumes from the last cursor it stored.
- Every writer of a revision serializes on one row. At this service's write rate that costs nothing measurable. A sustained write rate it could not carry would reopen the decision.
- A controller learns of a new revision, not of an agent moved away from it. Releasing an agent elsewhere is placement and lifecycle, issue #218.
- `schema.sql` changes `definitions` in place again, adding a position and the recorded assignment, and adds `positions` and `agent_status`. No database is deployed, so none is migrated.

## Signs the decision was wrong

- The controller routes and the console's begin sharing a principal or a refusal. The two domains would then be one surface with two doors.
- Withheld revisions have to be re-sent once a placement settles. The cursor would then need to carry per-agent state, which a single position cannot.
