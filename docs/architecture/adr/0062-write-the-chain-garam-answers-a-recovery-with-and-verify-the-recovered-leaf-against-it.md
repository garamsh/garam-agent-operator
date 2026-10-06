# ADR 0062: Write the chain garam answers a recovery with, and verify the recovered leaf against it

> Status: accepted
> Date: 2026-10-07

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0059](0059-place-a-recovered-credential-beside-the-kept-issuer-only-once-it-verifies-against-it.md) placed a recovered certificate beside the issuer and server root kept from the agent's first certificate. That was because garam's `RecoveredCredential` named neither at the pin, `59fe68d`. It placed nothing for an agent whose first credential was never placed.

**garam settled the gap.** It did so in `garamsh/garam#1223`, merged at `f54b9e8` with ADR-0091:
- **The fields.** `RecoveredCredential` now requires `issuerPem` and `serverRootPem`, the values the signer produced.
- **Retries.** garam stores them with the recovery, so a retry under the same `requestId` answers them identically.
- **The contract.** The change is additive to execution-fence.v1.
- **The edge garam recorded.** A retry of a recovery recorded before garam's migration 000036 is answered with the authority's *current* chain. No authority or server root has been replaced, so that is correct today.

Issue #306 takes the chain up. Issue #308 needs it: an agent stranded by ADR 0058 never had a first certificate, so it has no kept chain to verify a recovered certificate against.

The PM settled this on #306's dispatch.

## Decision

**The pin moves to `f54b9e8`.** `GARAM_REVISION` is `garamsh/garam@f54b9e8cda824dc06df86513e68b49556f120eef`, checked with `gh api repos/garamsh/garam/compare/f54b9e8...<rev>`. The control e2e suite runs against it.

**Control stores the answered chain and returns it.**
- **Stored.** The finalize stores the `issuerPem` and `serverRootPem` garam answers beside the recovered certificate, in migration 4's `recoveries.issuer_pem` and `server_root_pem`. They are null where garam answered none.
- **Returned.** The controller route `recovery-requests` returns them with the certificate once finalized, and the console's recovery read returns them too.

**The manager writes the answered chain, and prefers it.**
- **Where control answers a chain.** The recovered certificate must chain to the answered issuer, besides being over the persisted key and naming the agent's GRN. It is then written with that issuer and server root.
- **Where control answers none, which an older control does.** ADR 0059 holds: the leaf is verified against the issuer kept from the placed credential, and the kept chain stays.
- **The verification stays either way.** It covers garam's recorded edge: a chain that has moved under a pre-000036 retry would not verify, and is refused rather than written.

**An agent whose credential was never placed may now be recovered.**
- **With an answered chain.** The manager persists the request as for any recovery. Once finalized, it creates the credential Secret whole from the answered chain, verified against the answered issuer, with the lineage on it. That is #308's route for an agent stranded by ADR 0058.
- **With no chain answered.** Nothing is placed, and the request is kept.

## Consequences

- **A recovered credential carries the chain garam signed it under,** not the one kept from the first certificate.
- **An agent with no first certificate can be given one through recovery.** That is subject to garam issuing it, which #308 runs against a real garam.
- **A manager that meets an older control still places what it can.** It recovers beside the kept chain where one is placed.
- **ADR 0059's kept chain** now stands only for an answer naming none.
- **Ruled out.**
  - **Trusting the answered chain unverified.**
  - **Discarding the kept chain fallback.** That would break a manager meeting a control before this change.
