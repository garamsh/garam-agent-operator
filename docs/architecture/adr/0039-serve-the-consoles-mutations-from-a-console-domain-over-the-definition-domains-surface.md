# ADR 0039: Serve the console's mutations from a console domain over the definition domain's surface, and keep the request record with the revision it produced

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #211 adds the control service's first HTTP route, the console's configure. [ADR 0038](0038-partition-the-code-by-domain.md) left one question open, and `structure.md` §Open questions carried it: whether that HTTP surface is part of `internal/definition` or a domain of its own, settled against `structure.md` §A new domain.

The route does more than translate a request. Every console mutation passes the same checks, in this order (issue #211, against garam's operation-authority.v1 at `garam@fdfb76d`, read at `garam@f2ac780`):

- it introspects the `Authorization: Garam-Operation <authority>` handoff on garam's machine listener;
- it checks every field the authority binds — audience, organization, operation, target, assignment, expiry — against the request;
- it compares the SHA-256 of the exact body received with the digest the authority binds;
- only then does it find or create the request record keyed `(organization, request id)`, and apply the change.

That pipeline has its own vocabulary — authority, handoff, binding, operation reference, bound field, digest — taken from garam's contract rather than from desired state. It has a consumer outside the repository, the console, and its surface (the routes and their refusals) holds while garam's wire or the store behind it changes. Those are `structure.md` §A new domain's three conditions.

The request record could sit on either side. It holds the bound fields every repeat is compared against, and the outcome every repeat returns. The outcome is the revision the request stored, or the refusal of a stale one, and the record has to commit in the same transaction as that revision. Otherwise a crash between the two leaves a request that applied with no record, or a record with no revision.

## Decision

1. **The console's mutations are their own domain, `internal/console`.** It owns the operation-authority pipeline, the `Introspector` port to garam and its implementation in `internal/console/introspector/`, and the console's routes.
2. **The dependency runs one way: `internal/console` imports `internal/definition`'s root package, and nothing else of it.** `internal/definition` never imports `internal/console`.
3. **The request record stays in `internal/definition`, stored with the revision it produced.** `internal/definition` stores the bound fields — actor, operation, target, body digest, operation reference, assignment — as data, compares them on every repeat, and interprets none of them. Deciding what an authority permits is `internal/console`'s alone.
4. **A later surface is judged on the same test when it arrives, and is not folded into `internal/console` by default.** That covers the controller routes (`/v1/operators/self/...`, the operator's own principal) and the runtime-status route (`/v1/agents/{grn}/runtime-status`, the agent's certificate).

## Consequences

- `structure.md`'s open question moves into its current decisions, and `internal/console` is listed there as a domain (`docs/architecture/README.md` §Rules 7).
- `internal/definition`'s `Service` takes a request key and a binding on every console mutation. Configure replaces the bare update, so no route can change a definition without a request record.
- Garam's 500 and 503 are handled in one place, `internal/console/introspector/garam.go`, as garam's ADR-0050 places a listener's 5xx: introspection consumes nothing, so it is retried within a bound and then answered as undecided.
- Ruled out: an HTTP package under `internal/definition`. It would put garam's contract vocabulary inside the desired-state domain, and every later surface would join it there by default.

## Signs the decision was wrong

- A change to the authority pipeline keeps needing a change to `internal/definition`'s surface, or `internal/definition` starts branching on an operation's name.
- The controller or runtime-status routes, judged on the test, turn out to share the console's vocabulary. The domain would then be wider than the console's mutations, and its name would be wrong.
