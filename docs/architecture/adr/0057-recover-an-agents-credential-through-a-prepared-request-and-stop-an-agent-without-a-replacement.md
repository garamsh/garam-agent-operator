# ADR 0057: Recover an agent's credential through a request its controller prepares, and stop an agent without a replacement

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

#218 named two lifecycle paths that nothing built: (C) recovery of an agent's credential, and (D) a stop without a replacement. Issue #280 builds them. Garam's PM bound the wire on #280 (read at `garamsh/garam@6a1b654`, the same at the `59fe68d` pin):

- **(C) Recovery.** agent-execution.v1 does not specify it, and garam's ADR-0084:152-153 makes it the manager's. Garam's side is execution-fence.v1's `recoverAgentCredential`, `POST /agents/{agent}/credential-recovery` (`api/machine.yaml:1275-1334`).
  - **The body.** It takes `{requestId, epoch, certificateRequestPem}`.
  - **The handoff.** It is sent under an administrator's `agent:recover` handoff whose digest binds the body.
  - **Its answers.** A digest or binding mismatch is 409, and the handoff is not spent. A reused `requestId` is 409. A 503 means undecided.
  - **Prepare and finalize.** The route is single-stage on garam's side, so prepare and finalize are this project's orchestration: prepare here, then call garam's route once, at finalize.
- **(D) Stop without replacement.** It is not specified. "Unverified" is not a fence state anywhere in garam, and garam never stops a runtime (ADR-0084:154-156).
  - **The stop is ours.** Stopping the runtime and keeping it stopped, with no replacement activated, falls to this project.
  - **Garam's part.** `deactivateAgent`, `POST /agents/{agent}/activations/{activation}/deactivation` (`machine.yaml:1198-1231`), stops garam routing to the agent.

**Two facts shape (C).**
- **garam hashes the body it receives.** So control must send garam the exact bytes the handoff was minted over.
- **The key never leaves its holder.** The certificate request's key is the manager's, as for the first certificate (ADR 0042, #218).

The PM settled this side's routes, store, scope and crash handling on #280's dispatch, before anything was built.

## Decision

**(C) Recovery is four steps, each a stored stage, and garam is called once, at finalize.**
1. **Open.**
   - **The route.** `POST /v1/orgs/{org}/agents/{agent}/recovery` `{requestId, recoveryRequestId}`.
   - **Its authority.** It takes an `agent:recover` authority bound to that body. Control introspects it and never sends it to garam.
   - **Two request ids.** garam binds one request id to one digest, so the authority's `requestId` differs from `recoveryRequestId`, as the cutover switch's `configureRequestId` does (ADR 0050).
   - **What is recorded.** Control records the recovery `requested`, under the epoch of the agent's latest revision.
   - **One open per agent.** Another recovery while one is open is 409 `recovery_open`.
2. **Prepare, by the agent's controller.**
   - **The feed.** It carries `recovery: {requestId, epoch}` for an agent with an open recovery.
   - **The route.** The controller makes a key and a certificate request and sends them to `POST /v1/operators/self/agents/{agent}/recovery-requests` `{requestId, epoch, certificateRequestPem}`. That takes the session proof and the agent-bound proof, as the first-certificate route does.
   - **What is stored.** Control stores the request garam is to be sent, as canonical bytes: `{"requestId","epoch","certificateRequestPem"}` in that order, by `encoding/json`. The recovery is now `prepared`.
   - **Repeats.** A repeat is 202 while prepared, and 200 with the recovered credential once finalized. Another request is 409 `request_reused`, and another epoch 409 `epoch_superseded`.
3. **Read.**
   - **The route.** `GET /v1/orgs/{org}/agents/{agent}/recovery`, under `agent:execution-read` bound to the route.
   - **The answer.** It answers the recovery with the prepared `body`, as a string, and its `bodySha256`. The administrator mints the finalize handoff over these bytes.
4. **Finalize.**
   - **The route.** `POST /v1/orgs/{org}/agents/{agent}/recovery/finalize`. Its body is exactly the prepared bytes, under the `agent:recover` handoff with `requestId` = `recoveryRequestId`.
   - **The checks.** The console pipeline checks the handoff's digest against the received bytes. Control then requires those bytes byte-equal to the prepared ones, and otherwise answers 409 `recovery_mismatch` before garam is asked.
   - **The call to garam.** Control sends garam the same bytes under that handoff, as `Garam-Operation <handoff>`. garam resolves and spends the handoff itself (`internal/agent/execution_service.go`, `ResolveHandoff`), so control forwards it rather than its durable reference.
   - **garam's answers.** A 201 records the recovery `finalized` with `{lineage, certificatePem}`. A garam refusal is answered under garam's status and kind. Undecided is 503, and the recovery stays prepared.

**What a recovered certificate carries.** It is garam's `RecoveredCredential` alone, `{lineage, certificatePem}`. The issuer and server root of the first certificate are not reused, because garam's contract does not say a new lineage chains to them. The manager's half takes them as #300 settles it.

**(C)'s manager half is #300.** That half generates the key and the request, keeps them in a Secret before sending, fetches the certificate and installs it. Until it lands, a recovery stays `requested`, which harms nothing.

**(D) Stop and start, under `agent:configure`.**
1. **Stop.**
   - **The route.** `POST /v1/orgs/{org}/agents/{agent}/stop` `{requestId}`. garam's PM confirmed `agent:configure` as the high-trust permission (#283). `deactivateAgent` needs no member handoff: it is authorized as `activateAgent` is.
   - **What is recorded first.** The stop is recorded under the agent's activation lock, with the agent's latest activation, and from its commit:
     - the activation route refuses the agent, a repeat included, with `403 placement_not_current`. agent-execution.v1's refusal kinds are a closed list (garam ADR-0084), and that one pauses the adapter until its placement changes. So no replacement is activated, even from a placement registered later;
     - the feed carries `stopped: true`.
   - **Then garam.** Control calls `deactivateAgent` for the recorded activation, and records the deactivation on garam's 200 alone.
   - **Repeats and refusals.** A repeat of the request finishes a deactivation garam left undecided. Another stop of a stopped agent is 409 `agent_stopped`.
2. **Start.**
   - **The route.** `POST /v1/orgs/{org}/agents/{agent}/start` `{requestId}` ends the stop. With no stop it is 409 `agent_not_stopped`.
   - **What it changes.** The next activation is admitted, and the feed drops `stopped`.
   - **History is kept.** The stop's row is kept with `started_at`.
3. **The manager.**
   - **`spec.stopped`.** The renderer writes the feed's `stopped` into `spec.stopped`, which the API server refuses off the `Control` source, as `spec.revision` is refused. The puller renders an unchanged revision again when only `stopped` changed.
   - **No replica.** The reconciler asks for no replica on `spec.suspended` or `spec.stopped`, which reuses ADR 0046's suspension whole:
     - the writer fence (ADR 0042) releases the Pod only on evidence;
     - a Pod whose fence is `Unverified` stays held;
     - once released, no replacement is created;
     - `Suspended` and `Available` report it.
   - **A person's field stays a person's.** `spec.suspended` stays the person's (ADR 0046), which is why the stop has a field of its own.

**How (D) holds.**
- **A manager restart.** It re-reads the feed and the persisted spec.
- **A new placement registration.** It is recorded, and its adapter's activation is refused.

**The feed gains two optional fields** beside `origin`, an addition to ADR 0040's wire:
- `stopped: true` while a stop holds the agent, absent otherwise;
- `recovery: {requestId, epoch}` while a recovery is open, absent otherwise.

Opening, finalizing, stopping and starting each move the feed's position, so a long poll answers them at once.

**The store is migration 3:**
- `recoveries`, keyed by (agent, `recoveryRequestId`) and unique by its console request. A partial unique index admits one open recovery per agent.
- `stops`, keyed by its console request, with `started_at`. A partial unique index admits one current stop per agent.

**A crash between stages resumes from a contract-defined state only** (#288's rule):
- every stage is a stored row, and each step repeats;
- a finalize repeated after a lost answer sends the same bytes under a handoff minted again for the same request, which garam answers with the certificate it issued;
- a repeated stop calls `deactivateAgent` again, which garam answers 200 for an activation already ended;
- a 503 decides nothing, and control infers neither a recovery nor a deactivation from anything but garam's answer.

## Consequences

- **A deployment can stop an agent from the console and start it again,** with no replacement activated in between, whatever its fence says.
- **A recovery can be opened, prepared and finalized against garam once #300 lands.** Until then the console can open one, and it stays requested.
- **Two more refusals reach the adapter under an existing kind.** It reads a stopped agent as a placement that is not current. The message names the stop.
- **Ruled out.**
  - **Writing `spec.suspended` from the feed.** It is a person's field (ADR 0046).
  - **Refusing placement registration while stopped.** A placement is a fact the manager reports, and the activation is what admits a runtime.
  - **A new adapter-facing refusal kind for a stop.** agent-execution.v1 is garam's, with a closed list.
  - **Sending garam the durable reference at finalize.** garam's recovery resolves the handoff itself.
  - **Re-encoding the body at finalize.** The handoff binds bytes, not meaning.
