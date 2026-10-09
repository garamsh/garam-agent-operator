# ADR 0063: Keep an agent whose only creation the upgrade archived through credential recovery and a configure

> Status: superseded by ADR-0069
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0058](0058-refuse-the-first-certificate-of-an-agent-whose-only-creation-the-upgrade-archived-and-name-its-recovery.md) refuses the first certificate of an agent whose only creation the upgrade archived, `409 creation_archived`. The published `7c216469476d` kept no `agent:create` reference, and garam issues a first certificate only under the creation's own. Its message named two routes:
- **Re-creation**, which is proven and gives the agent a new GRN.
- **Credential recovery**, which garam's PM named as keeping the GRN, identity and memory. It was unproven for an agent that never had a certificate, and #308 tracked proving it.

Recovery needed both halves:
- the control service's, #305 ([ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md));
- the manager's, #300 ([ADR 0059](0059-place-a-recovered-credential-beside-the-kept-issuer-only-once-it-verifies-against-it.md)).

Such an agent has no kept chain, so it also needed #306 ([ADR 0062](0062-write-the-chain-garam-answers-a-recovery-with-and-verify-the-recovered-leaf-against-it.md)): the chain garam answers a recovery with, placed where no credential was.

**What `TestRecovery_GivesAStrandedAgentItsFirstCertificate` (`tests/control`) shows against garam `f54b9e8`:**
1. **The stranding.** garam registers a managed agent. Its creation and revision 1 are stored with `7c216469476d`'s statements, and the database is upgraded. Its first certificate is refused `creation_archived`.
2. **The recovery.** An owner opens a recovery. The manager's recoverer prepares it over a key it persists. The finalize is sent to garam under the `agent:recover` handoff.
3. **garam issues.** It issues a certificate naming the agent's own GRN, with the issuer and server root it answers. The recoverer verifies it against that issuer and places it where no credential was.
4. **Revision 1 cannot be activated.** It is refused `403 not_authorized`, "control holds no authority to activate this revision": its activation would be sent under the creation's reference, which was never kept.
5. **A configure runs the agent.** A configure stores revision 2 under its own `agent:configure` reference, and the recovered pair activates revision 2 under that GRN.

## Decision

**Credential recovery, followed by a configure, is the route that keeps an agent whose only creation the upgrade archived.**
- **What it keeps.** Its GRN, identity and memory.
- **The recovery.** An owner or admin opens it at `POST /v1/orgs/{org}/agents/{agent}/recovery` under `agent:recover`, and the manager prepares it. It is finalized under the `agent:recover` handoff over the prepared request.
- **The configure.** Its first activation needs a reference, so a configure (`POST /v1/orgs/{org}/agents/{agent}/revisions` under `agent:configure`) follows. Revision 2 is activated under that configure's reference, and revision 1 never is.

**Re-creation stays the alternative.** It gives the agent a new GRN, so its identity and memory do not carry over.

**The `creation_archived` message names recovery first, as the route that keeps the agent.** It names the configure that follows, and re-creation as the route that loses it. The refusal itself, its status and kind, and everything else ADR 0058 decided are unchanged.

## Consequences

- **An agent stranded by the upgrade has a supported way back under its own GRN.** ADR 0058's second route is no longer unproven.
- **The operator takes two console steps, a recovery and a configure,** where a re-created agent takes one. The trade is keeping the agent's memory.
- **Ruled out.**
  - **Activating revision 1 without a reference.** Control sends a null reference only for a revision the runtime reported effective under the agent's latest activation (`control.md`, `operationRef`), and this agent never ran.
  - **Leaving the remedy at re-creation.** It loses the memory the recovery keeps.
