# ADR 0058: Refuse the first certificate of an agent whose only creation the upgrade archived, and name its recovery

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #303, found in the `garamsh/garam#1196` run 4 acceptance (scenario 8, order 1).

**What the upgrade does to an earlier creation.** Migration 2 moves every creation the published `7c216469476d` stored into `creations_n1`, unchanged, because none carries the bound fields, the controller or the epoch ([ADR 0055](0055-change-the-control-services-schema-only-by-forward-only-versioned-migrations.md) §4, Q1). ADR 0055 says nothing reads the archive.

**What the certificate-request route needs.** The agent's creation, for its `operationRef`. An agent with no creation is answered 404 (`control.md`). So an agent registered under `7c216469476d` that had no certificate kept its definitions after the upgrade, and could never get its first certificate. Its `Agent` waited on `CredentialsSecretMissing`.

**Why the archived row cannot be used.**
- garam's `issueInitialCertificate` requires `operationRef` to be "the `agent:create` reference of this agent's own creation, rechecked as current" (`garamsh/garam@59fe68d` `api/machine.yaml:1647-1651`).
- `7c216469476d`'s `creations` table has no such column (`internal/definition/repository/migrations/000001_baseline.up.sql:47-60`, which repeats it).
- Its `beginCreation` and `registerCreation` stored none (`git show 7c21646:internal/definition/repository/postgres_queries.go`).
- The archive copies exactly those columns (`000002_organizations_and_routes.up.sql:181-198`).
- The only earlier `operation_ref` is a configure request's, which garam answers 403 here.

So control holds no reference garam would accept for such an agent.

**How such a row can exist.** `7c216469476d` serves no create route, and nothing in it calls `beginCreation`. The row in run 4 was seeded out of band, through that release's own statements, after garam's managed create. Only a row made that way can be in this state.

The PM's triage on #303 first directed issuance from the archive. That rested on the archive keeping a reference, which it does not. The PM then settled on this decision.

## Decision

**The first certificate of an agent whose only creation is an archived, registered one is refused, and the refusal names the recovery.**
- **Where.** The certificate-request route, after the session proof, the assignment check and the agent-bound proof, as before.
- **When.** No current creation names the agent, and a registered row in `creations_n1` does.
- **The answer.** `409 creation_archived`. Its message says the agent has no `agent:create` reference to ask garam under, and names both recovery routes below, the second as unproven.
- **What is asked of garam.** Nothing. No request is stored.
- **Everything else is unchanged.** An archived row that was never registered names no agent, and an agent with no creation at all still gets 404. A current creation is used as before, even where an archived row names the same agent.

**The archive's one reader.** `Repository.ArchivedRegistration` reads `creations_n1`, and nothing else reads any archive. A database where no earlier creation moved has no `creations_n1`, and the reader answers that no archived row names the agent.

**Two recovery routes, named in the message.**
1. **Re-creation, proven.** The operator deletes the agent's `Agent` and creates the agent again through the console's create route (`POST /v1/orgs/{org}/agents`), which stores the creation's reference. garam mints a new GRN for it, so the old identity and its memory do not carry over.
2. **Credential recovery, unproven here.** This is control's recovery ([ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md)), in four steps:
   - an owner or admin opens it at `POST /v1/orgs/{org}/agents/{agent}/recovery` under an `agent:recover` authority;
   - the agent's controller prepares it at `POST /v1/operators/self/agents/{agent}/recovery-requests`;
   - it is read at `GET /v1/orgs/{org}/agents/{agent}/recovery`;
   - its finalize at `POST /v1/orgs/{org}/agents/{agent}/recovery/finalize`, under the `agent:recover` handoff, sends garam's `recoverAgentCredential` (`garamsh/garam@59fe68d` `api/machine.yaml:1277-1287`).

   garam's PM named this route as keeping the agent's GRN, identity and memory. It has not been tried on an agent that never had a certificate, and the controller's half of the preparation is #300. `garamsh/garam-agent-operator#308` tracks proving it, and until it is proven, nothing here claims it works.

## Consequences

- **The case is visible and has a stated step.** Before, it was an unending 404 retried every 5 minutes.
- **No migration, no new column, and no value a check would need is guessed.** The archive stays unchanged.
- **garam will not answer an agent's own creation reference.** garam's PM declined: the reference is single-use, and returning it would make it replayable. Issuance under it stays out of reach.
- **If #308 proves credential recovery for this case,** the message's second route stops being unproven, and the docs and the message say so.

Ruled out:
- **Issuing from the archived row.** It holds no reference, and garam requires the creation's own.
- **Sending a configure request's reference instead.** garam answers a reference other than the creation's own `agent:create` with 403.
- **Asking garam for the creation's reference.** Declined by garam's PM: the reference is single-use, so returning it would make it replayable.
- **An operator-supplied reference recorded by a subcommand.** It would need a schema change, and in practice only whoever called `createManagedAgent` holds that reference.
