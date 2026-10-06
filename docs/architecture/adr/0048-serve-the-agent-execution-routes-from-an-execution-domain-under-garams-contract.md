# ADR 0048: Serve the agent's activation and runtime status from an execution domain, under the contract garam owns

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issues #218 (activation) and #211 (runtime status). garam owns the adapter-facing contract, `agent-execution.v1`, merged at `garamsh/garam@e81a1e0` (ADR-0084 there), and this service implements it. ADR 0039 §4 and `structure.md` judge each later surface on §A new domain when it arrives; these two routes are the runtime-status route it names, and activation beside it.

On §A new domain:
- **Lifecycle and vocabulary of their own.** The principal is the agent's leaf, not a user's handoff (`internal/console`) or a controller's leaf (`internal/distribution`). The vocabulary is activation, generation, execution token, anchor and fence.
- **Something outside depends on it.** garam's adapter, in every Pod the manager places it in.
- **A surface that holds while the inside changes.** `agent-execution.v1`, versioned by garam.

The PM settled the bindings on this dispatch, recorded on #218.

## Decision

**The agent routes are their own domain, `internal/execution`, over `internal/definition`.** `POST /v1/agents/{grn}/activations` and `POST /v1/agents/{grn}/runtime-status`:
- **Imports.** It imports `internal/definition`'s root package only. No other domain imports it.
- **Its own port to garam.** It declares its own port, `Garam`, for the execution introspection (B), the activation (A) and the agent-bound controller proof, implemented in `internal/execution/garam` over `internal/garammachine`. It does not reuse `internal/distribution`'s `Prover`: the two are siblings.
- **The records stay in `internal/definition`.** That is the activation requests, the agent's latest activation, and the revision the runtime applied, beside the placements and the status they are decided against.

**What changes for a rule.** `structure.md` §The control service's API names this domain beside the console's and the controllers'. Three domains now share `internal/garammachine`, and each declares its own port over it.

## Consequences

- **Each domain keeps its own principal.** A controller's leaf never reaches the agent routes' decisions except as the placement's stored leaf, proved again per attempt.
- **Two ports over one client.** The controller proof is asked through two ports, one per domain, each over `internal/garammachine`. The duplication is a call's worth of decoding, kept to hold the siblings apart.
- **A change to the contract** is garam's to make and is a new contract value. This side follows it, citing the commit.

Ruled out:
- **Folding the routes into `internal/distribution`.** The principal and the contract differ, and the controller routes' surface is C2 (ADR 0040), which this one is not.
- **Moving `Prover` below the domains.** It would make one domain's port the other's dependency, for one call.
