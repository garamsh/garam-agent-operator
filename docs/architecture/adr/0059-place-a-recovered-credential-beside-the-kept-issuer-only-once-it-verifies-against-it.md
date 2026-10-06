# ADR 0059: Place a recovered credential beside the issuer kept from the first certificate, only once it verifies against it

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0057](0057-recover-an-agents-credential-through-a-prepared-request-and-stop-an-agent-without-a-replacement.md) built the control service's half of a credential recovery and left the manager's half to #300:
- **Prepare.** The manager prepares the open recovery's certificate request over a key it holds.
- **Collect.** It collects the recovered certificate.
- **Install.** It installs that certificate into the agent's credential.

**The control service answers what garam answers.** A recovered credential arrives at the manager as garam's `RecoveredCredential`, `{lineage, certificatePem}` (`garamsh/garam@59fe68d` `api/machine.yaml:2700-2709`). Neither the contract nor that route's description (`machine.yaml:1275-1334`) names the issuer or the server root the leaf chains to. A first certificate's answer names both.

**The credential Secret holds four keys.** `certificate.pem`, `key.pem`, `issuer.pem` and `server-root.pem` (`internal/desired/credential/secret.go`). The adapter reads three of them:
- the certificate and the key, as its client pair;
- the server root, which verifies garam's listener (`garam@59fe68d` `internal/cli/cli.go:75-90`, `delivery.go:62-64,145`) and is a deployment's, unchanged by a recovery.

Nothing here reads `issuer.pem`.

**At the pin, garam's implementation signs a recovered certificate as it signs a first one.**
- **The same signer.** It goes through `requestSigner` (`internal/cli/execution.go:198-228`, "an agent's initial or recovered certificate"), whose issuer is the agent's organization authority (`internal/ca/service.go:120-145`).
- **No rotation.** "nothing rotates one yet" (`internal/ca/signing.go:20-22`).
- **Not the contract.** That is implementation, not contract. The PM took the question to garam's PM, as `garamsh/garam#1222`, tracked here as #306.
- **garam's answer.** `garamsh/garam#1223`, merged at `f54b9e8` (ADR-0091), adds `issuerPem` and `serverRootPem` to `RecoveredCredential`. That commit is past this repository's pin, `59fe68d`. So this decision is made at the pin, and #306 moves the pin and takes the fields up.

The PM settled this side on #300's dispatch.

## Decision

**The recovered certificate and its key replace the first ones; the issuer and the server root are kept.**
- **One patch.** The manager writes `certificate.pem` and `key.pem` into the agent's credential Secret, and records the lineage on it as the annotation `agent.garam.sh/credential-lineage`, in one merge patch.
- **What it leaves alone.** The patch names nothing else, so `issuer.pem` and `server-root.pem` stay as the first certificate placed them.

**It is placed only once it verifies against the kept issuer.**
- **What is checked.** The recovered certificate must be one PEM certificate that:
  - chains to `issuer.pem`;
  - is signed over the persisted key;
  - names the agent's GRN as its one SAN URI.
- **On a failure.** Nothing is written. The request Secret is kept, and the reason is recorded on it as `agent.garam.sh/recovery-refused: RecoveredCertificateUnverified`. The reconciler reports it on the `Agent`'s `Recovery` condition, under that reason.
- **No unverified Secret.** The manager never writes a credential Secret it could not verify.

**The manager's half runs as a `Runnable` of `internal/desired`, the recoverer, beside the issuer.**
- **What it takes.** It takes a step for every agent the feed names with an open recovery (the puller offers each answer's set whole), and for every agent with a persisted request.
- **Persisted first.** It generates an ECDSA P-256 key and a request, and persists them in `<agent>-recovery-request` under the recovery's request id and epoch, before sending them to `recovery-requests`. A restart sends the persisted request, never a new key. The request is persisted only for an agent whose first credential is placed.
- **Prepared.** On control's 202 it asks again every 10 seconds until an administrator finalizes.
- **Finalized.** On 200 it verifies the certificate and places it, and only then deletes the request Secret.
- **Old requests first.** A persisted request is finished before another is begun, because garam may have signed over its key.

**A recovered credential moves the Pod.** The Pod copies its credential once, at start (ADR 0010).
- **The trigger.** The reconciler carries the credential Secret's lineage annotation on the StatefulSet's Pod template, so a placed recovery rolls the Pod through the writer fence.
- **No change on upgrade.** A credential never recovered carries none, so no existing template changes.
- **After the roll.** The new Pod registers its placement, and its adapter activates under the new lineage on the existing route.

**The `Agent` reports `Recovery`**, for agents on the `Control` source alone:
- `True` `Recovering` while a request is persisted;
- `True` `RecoveredCertificateUnverified` when the certificate was refused;
- `False` `NotRecovering` otherwise.

The reconciler is that condition's one writer. It reads the request Secret's metadata only.

**Once the pin carries garam's fields (#306).** Control is to store them and the manager to prefer them. The verification stays either way: garam records that a retry of a recovery made before its migration 000036 is answered with the authority's current chain, and the check is what keeps that case safe.

## Consequences

- **A recovery completes end to end.** It runs console open, manager prepare, administrator finalize, manager place, and the Pod moves.
- **A recovered certificate from an unexpected authority is held, not installed.** The agent keeps running on what it has, and its `Recovery` condition says why.
- **The kept issuer is trusted until #306 takes up the chain garam now answers.** A rotation of the organization authority, which garam does not do yet, would surface as `RecoveredCertificateUnverified` rather than as an agent that cannot authenticate.
- **Ruled out.**
  - **Placing the certificate on garam's implementation behaviour alone, unverified.**
  - **Writing an issuer the contract does not name.**
  - **Deleting the request Secret before the credential is written.**
  - **A condition written by the recoverer.** The reconciler writes the `Agent`'s conditions, and the recoverer records its refusal on the Secret it owns.
