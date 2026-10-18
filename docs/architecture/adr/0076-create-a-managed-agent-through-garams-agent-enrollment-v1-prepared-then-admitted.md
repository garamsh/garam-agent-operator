# ADR 0076: Create a managed agent through garam's agent-enrollment.v1, prepared then admitted

> Status: accepted
> Date: 2026-10-19

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0073](0073-give-every-agent-garam-tokens-its-assignee-mints-in-place-of-a-certificate.md) decision 1 has the control service create a managed agent under `managed-enrollment.v2`: one call, `createManagedAgent` (`POST /operators/{controller}/managed-agents`), answering `{grn, epoch}`.

garam `37567a41305da74a3fab2ece97820bac29f5a5b5` (`v0.4.1`, the control e2e pin since #383) carries `agent-enrollment.v1` (garamsh/garam#1486, garam ADR-0102 §Common agent enrollment), the one admission contract both placements share. Its `api/machine.yaml` states:

- **Prepare.** `prepareAgentEnrollment`, `POST /agent-enrollment-approvals`, takes `AgentEnrollmentBinding {requestId, placement, assignee, operationRef, replaces?}` and answers `AgentEnrollmentApproval` with its `approvalId`. The first answer is 201, and the same binding under the same `requestId` answers the same approval with 200. Another binding under that `requestId` is 409 `binding_conflict`. A reference current state no longer authorizes is 403, and an unknown one 404. `placement: direct` is 409 `direct_placement_gated`, permanently.
- **Admit.** `admitAgentEnrollment`, `POST /agent-enrollment-approvals/{approval}/admission`, takes the same binding and answers `AgentAdmission {grn, placement, assignee, epoch, lineage, mode: fenced, credentialDelivery: mintAgentToken}`, 201 and then 200 for the same binding. A moved agent is 409 `superseded`, another binding 409 `binding_conflict` or `approval_spent`. It rechecks the reference as prepare does.
- **After admission.** `assignee` mints the agent's tokens with `mintAgentToken` in the epoch and lineage admission answered, and the first activation, under `execution-fence.v2`, carries the reference as `operationRef`.
- **Refusals.** Every refusal the contract names carries `reason` in its `Error` body.

garam's PM confirmed two points against garam `28754663e27e20dd43c9451d53829ba112ed23ad` `internal/agent/admission_service.go`, an ancestor of the pin, on issue #377 (2026-10-18):

- **The approval is keyed by (organization, `requestId`).** Preparing again answers the stored approval, so nothing has to be kept between the stages. Both stages re-read the `agent:create` reference from current state, so one that expired or was revoked in between is refused on the retry even though the approval exists.
- **Admission writes the execution's lineage as the `lineage` it answers**, and `mintAgentToken` mints in it.

[ADR 0068](0068-keep-no-backward-compatibility-path-during-development.md) keeps no backward-compatibility path during development.

## Decision

1. **Creation is prepare, then admit, under `agent-enrollment.v1`.** `internal/definition/registrar/garam.go` sends `{requestId, placement: managed, assignee: <controller>, operationRef}` to `prepareAgentEnrollment`, then the same binding to `admitAgentEnrollment` for the approval it answered, and stores the admission's `{grn, epoch}` as ADR 0073's creation did. `managed-enrollment.v2` is not called, and no fallback to it is kept (ADR 0068).
2. **Placement is always `managed`, and `replaces` is never sent.** This service creates new agents only. garam refuses `direct` permanently, and a replacement is a decision of its own.
3. **No approval identifier is stored.** Every registration, a repeat or the resumption of a `Pending` creation included, prepares again under the creation's `requestId` and admits under the approval that answer names. A crash between the stages is therefore recovered by the creation's next repeat.
4. **A reference refused on a retry ends the creation through the existing refusal.** A 403 or 404 at either stage wraps `definition.ErrRegistrationRefused`, so a `Pending` creation becomes `Failed` and later repeats answer that refusal without asking garam. Recovering needs a new `agent:create` reference under a new `requestId`, a new creation. Nothing here retries a refusal.
5. **Refusals map onto the existing `definition` errors**, with garam's `reason` named in the error where its body carries one:

   | garam answers, at either stage | `definition` error |
   |---|---|
   | 403, 404 | `ErrRegistrationRefused` |
   | 409 (`binding_conflict`, `approval_spent`, `superseded`) | `ErrRegistrationConflict` |
   | 500 or 503 on every attempt, or no answer | `ErrRegistrationUndecided` |
   | a decided answer under another contract, or none | `ErrGaramContractUnsupported` |

   A conflict on a creation already `Registered` is `ErrAssignmentMoved`, as before: `superseded` is garam's word for the same fact.
6. **The admission's `lineage` is not stored.** The manager mints the agent's tokens with `mintAgentToken`, which mints in the lineage the agent's execution holds, and admission writes the execution's lineage as the one it answers. The two are one value, so the token-mint path is unchanged.

## Consequences

- ADR 0073 decision 1's contract, `managed-enrollment.v2`, is superseded by decision 1 here. The rest of ADR 0073 decision 1 stands.
- Each creation costs two calls to garam where it cost one.
- The control e2e suite creates the agent it seeds for configure through the same two stages, under the hosted operator's token, and no longer presents the hosted operator's certificate.

## Rejected alternatives

- **Keep `managed-enrollment.v2` beside v1.** ADR 0068 keeps no such path, and `createManagedAgent` and admission would be two ways to create the same agent.
- **Store the approval identifier with the creation.** Preparing again answers the same approval, so a stored identifier would add a migration and a second source for a value garam already keys.
- **Treat a refusal on a retry as undecided and retry it.** The refusal is current authority's decision about this reference, and no retry under the same `requestId` changes it.
