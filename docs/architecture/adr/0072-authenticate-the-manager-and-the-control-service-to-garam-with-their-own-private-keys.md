# ADR 0072: Authenticate the manager and the control service to garam with their own private keys, and drop the operator certificate

> Status: accepted
> Date: 2026-10-14

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The manager and the control service each authenticated to garam's machine listener with a client certificate:
- The manager obtained its own by enrolment with a one-time token ([ADR 0020](0020-enroll-this-operator-with-a-one-time-token-and-keep-the-key-it-generated.md), [ADR 0021](0021-present-any-one-enrollment-token-once-and-wait-for-another.md), [ADR 0022](0022-end-the-enrollment-on-a-certificate-that-has-not-expired.md)).
- It renewed it into the Secret it read it from ([ADR 0008](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md)), holding `patch` on that Secret.
- It exported the certificate's expiry as a metric ([ADR 0030](0030-carry-the-operators-certificate-expiry-and-garams-refusals-as-metrics.md)).
- The control service was handed its certificate as two files.
- The manager reached the control service's controller routes with the same certificate. The control service forwarded that leaf to `introspectController` under `operation-authority.v1` ([ADR 0040](0040-release-desired-state-to-controllers-from-a-distribution-domain-each-decision-proved-by-garam.md), [ADR 0043](0043-pull-managed-agents-desired-state-from-the-control-service-and-keep-each-grn-on-one-source.md)), and stored it with each placement for activation to prove the controller over.

garam `9ba35c3` (garam ADR-0102, `garamsh/garam#1360`) gives a registered operator a key it is issued once:
- `issueOperatorKey` (`api/openapi.yaml`) answers a PKCS#8 RSA `privateKeyPem`, the operator's `clientId` and the `tokenUrl`.
- `exchangeClientAssertion` (`POST /oauth/token`, `api/machine.yaml`) trades an RS256 client assertion the key signed for an opaque token under `machine-token.v2` (`private_key_jwt`, RFC 7523). It answers `expires_in` 3600, `invalid_client` at 400, and 429 with `Retry-After`.
- An optional `audience` mints the token for another operator of the organization. `introspectController` under `operation-authority.v2` takes that token in place of `certificatePem` (`api/machine.yaml:1418-1424`). garam answers 422 for a token that is not live, names another controller or names another audience. The sentence at `api/machine.yaml:252-253` saying no route offers this is stale, as garam's PM confirmed on #354.

Each operation a token reaches is listed with `machineBearer`, and its contract values move to `.v2`. On the cutover routes the operation reference moves to a `Garam-Operation` header.

The calls bound to an agent's credential are different: managed create and the initial certificate, activation, execution introspection and credential recovery. Under a token they need an agent's token, or carry another body, and agents hold certificates until #355. [ADR 0068](0068-keep-no-backward-compatibility-path-during-development.md) keeps no dual path.

## Decision

1. **Identity.** The manager and the control service are separate registered operators. Each reads three files, which a person places from one `issueOperatorKey` answer into a Secret the binary mounts read-only:
   - the private key;
   - the `client_id`;
   - the `tokenUrl`.

   Neither binary writes those files or holds RBAC on that Secret.
   - The manager reads them through `--garam-private-key-file`, `--garam-client-id-file` and `--garam-token-url-file`.
   - The control service reads them through `--operator-private-key-file`, `--operator-client-id-file` and `--operator-token-url-file`. Its `client_id` is the audience every operation authority must name.
2. **Exchange.** `internal/machinetoken`, below both binaries' domains, signs each assertion with `go-jose` v4. Each exchange:
   - reads the key file again, and derives `kid` (the RFC 7638 SHA-256 thumbprint) from the key it signs with;
   - sets the protected header to `alg` RS256, `kid` and `typ` JWT, and nothing else;
   - sets `iss` = `sub` = `client_id`, and `aud` to the `tokenUrl` as one string;
   - sets `exp` 60 seconds after `iat`, and a `jti` of 256 random bits;
   - is sent to `/oauth/token` on the listener the binary dials, which need not be `tokenUrl`.

   The token answered is presented as `Authorization: Bearer`. Its handling:
   - It is replaced once two thirds of its lifetime have passed, and a 401 from a protected call re-exchanges once with a new assertion.
   - A 429 waits its `Retry-After`. Any other failure waits from 1 second, doubling to 5 minutes, and the token held is presented while it lives.
   - `invalid_client` is reported by that name, and nothing else is tried in its place. No key, token or assertion is logged or put in an error.
3. **Manager to control service.** The manager exchanges a second token, with `audience` = `--control-grn`, and sends it as `Authorization: Bearer` with its own GRN in `Controller-Grn`.
   - The control service refuses 401, without asking garam, a request with no bearer token of garam's operator form or no GRN.
   - Otherwise it calls `introspectController` for that GRN under `operation-authority.v2`, presenting its own token and forwarding the manager's as `token`.
   - The proof must name that GRN, and is taken per decision as ADR 0040 decides.
   - garam's 422, a token that is not live or names another controller or audience, is answered 401, so the manager's transport exchanges one fresh token and asks once more. Without it, a token revoked by a key's rotation or withdrawal would be refused until the manager's own refresh, up to about 40 minutes later. garam's 403 and 404 refuse the authority, not the token, and stay 403.
   - The API listener requests no client certificate.
4. **Contract values.** The calls kept are sent under a token:
   - `introspectOperationAuthority` and `introspectController` under `operation-authority.v2`;
   - the four cutover routes under `agent-cutover.v2`, with the reference in `Garam-Operation`;
   - `deactivateAgent` under `execution-fence.v2`;
   - the manager's `listDefinitions`, `claimDefinition`, `issueAgentCertificate` and `reportProvisioningState`, which take no contract header.
5. **The calls bound to an agent's credential stay as they are** until #355: bodies and contract values unchanged, the recovery's handoff carried in `Garam-Operation`. garam refuses them beside a token, so creation, activation, execution introspection and credential recovery are unavailable on `dev` from this change until #355 lands.
6. **No controller leaf is stored.** A placement keeps no leaf, and the manager no longer re-registers a placement when its leaf changes.
   - Activation's agent-bound controller proof answers `ErrControllerUnprovable` without calling garam: an activation carries no token of the controller's.
   - Activation is then refused 403 `not_authorized`, with a message naming #355. The column is removed from the first migration ([ADR 0069](0069-keep-no-upgrade-path-for-the-control-services-store-during-development.md)).
7. **Removed.** The manager's enrolment, renewal and credential store, its mutual TLS, their five flags, the `garam-credential-role` Role and RoleBinding, and the control service's two certificate flags.
8. **The signal an alert depends on** is `garam_operator_token_exchange_failures_total{audience, reason}`. It counts each failed exchange by `audience` (`garam` or `control`) and by `reason`:
   - `key`, `invalid_client`, `throttled`, `refused` or `unavailable`.

   It replaces `garam_operator_certificate_not_after_timestamp_seconds`. `garam_operator_refusals_total` loses the `handshake` kind and the `renewer` and `enroller` runnables.

This supersedes ADR 0008 and ADR 0022, and ADR 0030's certificate-expiry gauge; ADR 0030's refusal counter stands, with the labels decision 8 amends. ADR 0020 and ADR 0021 were already superseded; the enrolment they and ADR 0022 describe is removed here. It supersedes ADR 0040's controller principal, the client certificate, and ADR 0043's client certificate.

## Consequences

- **The onboarding order, for each of the manager and the control service:**
  1. register the operator;
  2. issue its key;
  3. place the PEM, `client_id` and `tokenUrl` in its Secret;
  4. roll the binary.

  The manager also needs `--control-grn` wherever `--control-address` is set, and the key files wherever either address is set.
- **A key is rotated by placing a new one.** The next exchange signs with it, and nothing restarts. An issued key cannot be recovered from this side: a lost one is issued anew by a person.
- **Between this change and #355, `dev` cannot create, activate or recover a managed agent.** The PM holds promotion to `main` until #355 lands.
- **A control database an earlier build migrated is recreated empty.** It records version 1, so this binary migrates nothing, and its `placements.leaf_der` is `NOT NULL` while this binary writes no leaf: every placement registration would fail. ADR 0069 keeps no upgrade path, so the column is removed from migration 1 rather than by a migration 2.
- **The control service's e2e suite needs garam at `9ba35c3` or later.** The suite issues keys through garam's public API, and still enrols both operators with certificates to make, as a fixture, the agents and activations the binary can no longer make. Its creation, activation and recovery cases skip, naming #355.
- **A failed exchange stops nothing.** The token held goes on being presented until it expires, and the counter rises before every call starts failing.

## Rejected alternatives

- **A switch between certificate and token authentication.** ADR 0068 keeps no compatibility path, and garam refuses a request presenting both.
- **`machine-token.v1`'s shared credential (`exchangeToken`, `Garam-Credential`).** A secret garam holds the hash of authenticates the same identity a key does, and a stolen copy is a credential. The key's private half never leaves the cluster, which is what #354 asks for.
- **A hand-written JWS signer.** A stock JOSE library (`go-jose`, the one garam verifies with) is used instead.
- **Storing the manager's control-audience token on the placement,** re-registered whenever it rotates, so activation could prove the controller over it. That keeps a bearer token at rest in PostgreSQL. #355 redesigns the activation path around agents' tokens, and the PM decided on #354 that the proof waits for it, refused by name.
- **Keeping client certificates requested on the control service's listener for the agent routes.** Those routes cannot succeed under a token until #355 either, and the controller routes would go on requesting one.
