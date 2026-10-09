# ADR 0071: Render garam's issuer into a managed agent's config, and let the agent fetch its keys

> Status: accepted
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

garam's adapter is about to sign every message it hands an agent (`garamsh/garam#1258`, garam ADR-0101 step 4). garam publishes its verification keys at `GET /message-signing-keys` on its machine listener (`garam@5f178278:api/machine.yaml:776-783`), served under garam's private server root (garam ADR-0040). On `sherlock` `v0.3.0`, an agent with no `issuers` configured refuses a signed message `403 signature rejected: issuer` (#337).

What `sherlock` accepts (`sherlock@v0.3.0:docs/architecture/deployment.md:104,192-195,209`):
- `issuers` is set in the config file alone, and `SHERLOCK_ISSUERS` fails startup.
- Each entry names `issuer`, which is compared exactly with a signature's `iss`, and exactly one key source:
  - `keys-file`, a JWK Set a launcher renders and replaces within two hours of garam's set changing;
  - `keys-url`, an https URL the agent fetches itself.
- Changing the list is a restart.

`v0.3.0`'s `keys-url` trusts the system roots alone, so it cannot verify garam's private root. The `sherlock` PM accepted `garamsh/sherlock#1008`, an optional per-issuer `keys-ca-file` valid only with `keys-url`:
- a fetch trusts exactly that bundle;
- the bundle is re-read at each fetch, so a rotated root needs no restart;
- an unknown `kid` refetches the set at most once a minute.

It ships in the `sherlock` release after `v0.3.0`, and `v0.3.0` refuses the key at startup.

This operator already gives the adapter garam's machine address (`--garam-address`) and the garam server root. The root is `garam.ServerRootKey` in the copy of the agent's credential, which both the adapter and the agent container mount.

## Decision

**Where the manager is given garam's issuer and the adapter is placed, the agent's config file carries one static `issuers` entry:**

    issuers:
      - issuer: <--garam-issuer>
        keys-url: https://<--garam-address>/message-signing-keys
        keys-ca-file: /run/sherlock/credentials/server-root.pem

- **`issuer`** is a new manager flag, `--garam-issuer`: exactly garam's `machine.issuer`, an https origin with no path and no trailing slash. The manager refuses any other form at start, because `sherlock` compares it exactly.
- **`keys-url`** is derived from `--garam-address`, the address the adapter reaches garam's machine listener at, and never from the issuer. In-cluster, that address can be a Service name while the issuer is a public origin.
- **`keys-ca-file`** is `garam.ServerRootKey` in the copy of the agent's credential that the agent container already mounts at `/run/sherlock/credentials`. It is the file the adapter reads as its server root, from the same volume. No mount is added.
- **Which agents.** Every agent whose Pod carries the adapter (`adapterBuilt`), on either source, since both sources run the adapter that signs. Rendering it for `Control`-source agents alone would leave a signing adapter in front of a `Garam`-source agent that refuses its messages.
- **Without the flag, or with no adapter placed,** nothing is rendered, and the Pod is as before.
- **Nothing copies keys, and nothing re-renders.** Rotation of a key or of the root is the agent's to follow.

Enrolment, certificate issuance, the placement token and mTLS are unchanged (garam ADR-0100 O1 is separate).

## Consequences

- **The manager gains one flag,** which the deploying overlay owns (`configuration.md`).
- **The flag is set only once the cluster's agents run the `sherlock` release after `v0.3.0`.** An agent on `v0.3.0` refuses `keys-ca-file` at startup, and so fails to start once the flag is set.
- **The agent container reaches garam's machine listener over TLS** at a configured destination, which fits `sherlock` ADR 0013.
- **`server-root.pem` must stay in the agent's credential copy.** Both the adapter's TLS to garam and `keys-ca-file` read it. When garam ADR-0100's O1 later removes the certificate files from that copy, the server root has to remain, or `keys-ca-file` must move to wherever the root then is, in the same change.
- **Changing the issuer is a restart of every agent.** The entry is part of the config file, so a changed flag rolls each Pod once.

## Rejected alternatives

- **`keys-file` with a key-copying loop.** The operator would fetch garam's set and re-render it into each agent's config within two hours of every change, on a timer. A first key, or a key retired at once, would need an immediate re-render (garam ADR-0101). `keys-ca-file` removes the need: the agent fetches the set itself under the root it is told, and refetches on an unknown `kid` (`garamsh/sherlock#1008`).
- **`keys-url` built from the issuer.** The issuer is what a signature names, not where the agent's network reaches garam. In-cluster that can be another host.
- **A second mount of the server root alone into the agent container.** The agent already mounts the copy holding it, so a second mount adds a volume and a path and buys nothing.
- **Hard-coding the issuer.** It differs per deployment, and it is garam's chart value.
