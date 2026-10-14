# ADR 0073: Give every agent garam tokens its assignee mints, in place of a certificate

> Status: accepted
> Date: 2026-10-14

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Every agent this operator holds authenticated to garam with a client certificate:
- A `Garam`-source agent's came from `issueAgentCertificate` at construction ([ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md)).
- A managed agent's first came from the control service's `issueInitialCertificate`, over a key the manager generated. A recovered one came from `recoverAgentCredential`, verified against the issuer kept from the first ([ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md), [ADR 0059](0059-place-a-recovered-credential-beside-the-kept-issuer-only-once-it-verifies-against-it.md), [ADR 0062](0062-write-the-chain-garam-answers-a-recovery-with-and-verify-the-recovered-leaf-against-it.md)).
- An init container copied the credential, once, into a memory volume the Pod's user owns, because garam's key reader refused any other file ([ADR 0010](0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md)). The placement token took the same route ([ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md)).
- The adapter presented the agent's leaf to the control service over mutual TLS, and the control service forwarded it to garam.

[ADR 0072](0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md) moved the manager and the control service to tokens and left the calls bound to an agent's credential unavailable until this decision. Its decision 6 refused activation by name, because the control service holds no controller token to prove the controller with.

garam `afde8b8` (garam ADR-0100, garam#1352) gives an agent tokens in place of a certificate:
- **The mint.** The assignee mints the agent's tokens with `mintAgentToken` (`POST /agents/{agent}/tokens`, `machine-token.v1`), presenting its own token. garam answers only the operator holding the agent at the current epoch, and mints in the lineage the agent's execution holds. A token minted with the control service as its `audience` is accepted only by that service's forwards. A mint voids no earlier token.
- **The adapter's token mode** (`docs/architecture/adapter.md` §Token mode). `GARAM_ADAPTER_TOKEN_FILE` selects it, `GARAM_ADAPTER_CONTROL_TOKEN_FILE` goes beside the control settings and nowhere else, and naming the TLS certificate or key beside the token file refuses to start.
  - The adapter reads each file before every call and never caches it.
  - The writer replaces a file by rename; a Secret volume does.
  - A file that is missing or malformed leaves the call undecided.
  - "The writer keeps each file readable by the adapter's account alone; the adapter does not check the mode."
- **The refresh window** (garam ADR-0100 §Decision): a random point from two thirds to three quarters of a token's life. That leaves at least a quarter of the lifetime for the new token to reach its file: 15 minutes at the one-hour default. A Secret update reaches a mounted volume after the kubelet's sync period and cache delay, about a minute by default.
- **The control service's contracts.**
  - `managed-enrollment.v2`: creation opens the agent's lineage.
  - `execution-fence.v2`: activation and execution introspection forward the agent's token minted for the caller, as `token`. Recovery takes `requestId` and `epoch`, signs nothing, and answers `grn` and `lineage`.
  - `agent-execution.v2`: the adapter presents its control token in `Authorization: Garam-Token` and the placement token in `Garam-Placement`.
  - garam answers a token's introspection `current` only while it is inside its dates, minted at the agent's current assignee and epoch, and in its current lineage. It answers 422 for a token that is not this agent's for the caller: one minted for garam itself among them.

## Decision

1. **Creation.** The control service creates a managed agent under `managed-enrollment.v2`. The first-certificate route, its `initial_certificates` table (removed from migration 1, [ADR 0069](0069-keep-no-upgrade-path-for-the-control-services-store-during-development.md)), `issueInitialCertificate` and the manager's issuer are removed.
2. **Tokens.** The manager's refresher mints the tokens of every `Agent` carrying `spec.identity`, on either source:
   - a token with no audience, which garam accepts;
   - on a `Control`-source agent, whose adapter is fenced, a second one with `--control-grn` as its audience.

   Each is minted under the manager's own token, so no agent token is exchanged for its successor.
   - **Written.** They go into the agent's credential Secret, `<agent>-credentials`, under `token`, `control-token` and `server-root.pem`, with the refresh time as `agent.garam.sh/tokens-refresh-at`, in one write. Where the control service's token is among them, the write also sets `agent.garam.sh/tokens-fenced`; a fenced agent's Secret without it, as a cutover to the control service leaves one, is due at once.
   - **Guarded.** The write names the version of the Secret the refresher read, so a recovery recorded in between refuses it.
   - **Refreshed.** It is due at a random point in garam's window, measured from the mint to the earlier expiry. No interval of this operator's own is added.
   - **Refused.** A mint garam refuses (403), a token minted at another epoch than `spec.identity.assignmentEpoch`, a fenced agent with no `--control-grn` to mint for, and a spec epoch that is not an integer, are reported by name and written nowhere. The agent is asked about again after 5 minutes, never at a refresh's pace. A failure that belongs to one agent stops that agent alone: every other agent is still refreshed.
   - **Kept across a failed attempt.** garam keeps an agent's newest five tokens per audience, so a token an attempt minted and could not write is written by the next attempt rather than minted again. It holds only while the Secret's version stays as it was read, so a recovery recorded in between discards it.
   - **Construction.** A `Garam`-source agent's construction mints its first token in place of asking for a certificate, and the mint is what proves the epoch it records.
3. **Delivery.** The adapter mounts the credential Secret read-only at `/run/garam/credentials`, and the placement token's Secret at `/run/garam/placement`, each at mode 0440 under the Pod's group. The kubelet replaces both by rename.
   - The agent container mounts `server-root.pem` alone, out of the same Secret, where it is given garam's issuer ([ADR 0071](0071-render-garams-issuer-into-a-managed-agents-config-and-let-the-agent-fetch-its-keys.md)). It mounts no token.
   - The init container copying the credential, and both memory volumes, are removed.
   - **The boundary.** A token is confined by the containers that mount its volume, not by its file mode. Every container of the Pod runs as one user (65532), so a copy owned by that user at 0600 would exclude none of them, and only the adapter mounts the tokens.
4. **The adapter's settings.** `GARAM_ADAPTER_TOKEN_FILE` and `GARAM_ADAPTER_SERVER_ROOT_FILE` on every adapter. A fenced adapter also gets `GARAM_ADAPTER_CONTROL_TOKEN_FILE`, beside the control settings. `GARAM_ADAPTER_TLS_CERT_FILE` and `GARAM_ADAPTER_TLS_KEY_FILE` are rendered nowhere. `GARAM_ADAPTER_PLACEMENT_TOKEN_FILE` keeps its name and file.
5. **The agent routes** serve `agent-execution.v2`. A request without a token of garam's agent form is refused 401. The token is forwarded to `introspectAgentExecution` and `activateAgent` under `execution-fence.v2`, and stored nowhere. garam's 422 is answered `credential_fenced`.
6. **The controller's proof at activation** is garam's answer about that token: credential `current`, the agent assigned to the placement's controller, at the placement's epoch.
   - Each mismatch is refused by name: `credential_fenced`, `placement_not_current` or `epoch_superseded`.
   - Only the agent's current assignee can mint a token garam answers `current`, so a current token minted for this service proves the controller that holds the agent.
   - `introspectController` is not called at activation, and no controller token is stored. `activateAgent` still checks this service's own grant for that assignee.
7. **Recovery.**
   - **Request.** The manager's prepared request keeps ADR 0057's shape without a key or a certificate request: `{requestId, epoch}`, persisted before it is sent. The control service sends garam those bytes under `execution-fence.v2`, with the handoff in `Garam-Operation`, and records `lineage`.
   - **Answer.** Once finalized, the manager records the lineage on the credential Secret and removes its refresh time, in one write. The refresher mints the agent's tokens in the new lineage on its next pass. The Pod template carries the lineage, so the Pod moves to a new placement, as garam requires of a recovered agent.
   - **Removed.** The recovered leaf's verification and placement, and the `RecoveredCertificateUnverified` reason.
8. **The refusal counter** `garam_operator_refusals_total` counts the refresher's refusals under `runnable="refresher"`.

This supersedes [ADR 0010](0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md), [ADR 0059](0059-place-a-recovered-credential-beside-the-kept-issuer-only-once-it-verifies-against-it.md) and [ADR 0062](0062-write-the-chain-garam-answers-a-recovery-with-and-verify-the-recovered-leaf-against-it.md).

It also supersedes parts of six others:
- [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md)'s certificate route and the credential it places;
- [ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md)'s key and certificate request in the prepared request, and the recovered certificate;
- [ADR 0034](0034-place-garams-adapter-as-a-native-sidecar-beside-every-agent-this-operator-constructed.md)'s credential copy the adapter mounts, and its certificate and key settings;
- [ADR 0042](0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md)'s delivery of the placement token by copy;
- [ADR 0072](0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md)'s decision 5, and the refusal of activation in its decision 6.

The writer fence (ADR 0042, [ADR 0061](0061-release-a-pod-whose-writers-were-never-created-only-on-the-kubelets-terminal-phase.md)) is unchanged.

## Consequences

- **The minimum adapter image is garam-server built from `afde8b8`.** An older adapter refuses the token file settings, and its agents do not start.
- **An existing workload is not migrated.** The template no longer names the `credentials` init container's volumes, and no step removes the container from a StatefulSet an earlier build made, so its update is refused. The lab is recreated, as [ADR 0068](0068-keep-no-backward-compatibility-path-during-development.md) leaves it. A control database an earlier build migrated is recreated empty, as ADR 0072 recorded, because migration 1 changed.
- **A hand-written `Agent` mounts no credential.** Its credential Secret is still waited for, as before, and reaches no container.
- **Recovery stops a running agent's protected work until its Pod moves.** garam refuses the old lineage's tokens at once. The new tokens arrive within a refresher pass and the kubelet's propagation, and the Pod's next generation activates.
- **A refresher that cannot mint leaves the adapter on the tokens it has.** garam keeps a token accepted until its own expiry, so a refresh that fails is the margin's to absorb. The refusal counter rises before the adapter's calls start failing.

## Rejected alternatives

- **An owner-only copy kept current by a sidecar in the Pod.** It meets a mode the adapter does not check, and confines nothing more, since every container runs as the one user, at the cost of a container and a copy loop.
- **Storing the manager's control-audience operator token with each placement**, so activation could call `introspectController`. That keeps a bearer token at rest, which #354 and this issue rule out, and garam's answer about the agent's own token already names the assignee.
- **A dual certificate and token mode, or a certificate fallback.** ADR 0068 keeps no compatibility path, and garam's adapter refuses both modes together.
- **Refreshing at half the lifetime.** garam's contract sets the window and the margin it leaves.
- **Dropping the prepared recovery request** now that it carries no key. The manager still learns the finalized lineage only by asking under the request's identifier, and a feed that stops naming a finalized recovery leaves a restarted manager nothing else to ask with.
