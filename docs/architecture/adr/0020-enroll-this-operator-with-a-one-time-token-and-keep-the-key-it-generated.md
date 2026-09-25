# ADR 0020: Enroll this operator with a one-time token, and keep the key it generated

> Status: superseded by ADR-0021
> Date: 2026-09-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0008](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md) decided where this operator's credential lives and what replaces it, and it rests on a credential already being there: "the out-of-band mint places the Secret once and the operator's own renewal is the only other writer." **That first placement is the step this operator could not take.** It comes from `garam issue-certificate`, which reads the key-encryption key and so can be run only by the owner of the deployment, and `gitops#222` tracks it as a manual step for every operator.

`garam` closed that on their side. `garam@b16a896:docs/architecture/adr/0064-an-operator-enrols-with-a-one-time-token-and-keeps-its-key-in-its-own-cluster.md` (`accepted`) decides that registering an operator mints a one-time token, that the operator generates its own key pair and sends a certificate signing request presenting the token, and that `garam` signs a key it never held. The route is `POST /enrollment` on the machine listener, carrying `security: []` (`garam@b16a896:api/machine.yaml:147-198`), and the listener now asks for a client certificate rather than requiring one (`garam@b16a896:internal/machine/listener.go:29`) — which is what leaves a caller that has none able to reach one route.

Four facts of that contract bound what could be built here, read at the specification rather than taken from a summary.

- **The request is PKCS#10 over an ECDSA P-256 key, and `garam` reads two things out of it**: the public key, and the signature proving the sender holds the private half. A subject, a SAN or an extension request in it is discarded (`garam@b16a896:api/machine.yaml:168-174`), so no identity goes in and none comes back out of it: what the certificate names is the token's to say, and the answer's `grn` is where this operator learns its own.
- **The answer carries no private key**, deliberately and not by omission. `garam` generated none and can answer no second copy (`garam@b16a896:api/machine.yaml:813-820`).
- **A token is one attempt and not one certificate.** It is spent by the first call that presents it whether or not that call goes on to succeed, and a token that is refused is one to register again for rather than one to retry with (`garam@b16a896:api/machine.yaml:160-166`).
- **A refused token is one answer for three causes.** Spent, expired and never-minted are indistinguishable, because spending is one conditional delete (`garam`'s ADR-0064 decision 8), and a token stops being spendable ten minutes after it is minted.

`garam`'s ADR-0064 §Consequences names the half this repository owes and this is it. Nothing outside this repository's tests generated a key before it. At `cb99f95`, four files that are not tests import `crypto/` — between them `crypto/tls`, `crypto/x509` and `crypto/sha256` — while `ecdsa` and `elliptic` appear only in the stub listener the tests mint certificates with, and `CreateCertificateRequest` appears nowhere.

## Decision

**This operator obtains its first certificate itself, by presenting a token a person minted by registering it, and writes what comes back through the store ADR 0008 already decided.** `internal/garam/credentialstore` is the one home, so the renewal reads what the enrollment wrote and nothing copies a credential between two of them. ADR 0008's reasoning is unchanged and its "nothing seeds it" is now narrower: the credential has a second writer, and it is this operator rather than a person.

**The key pair is generated in the cluster and the private half never leaves it.** ECDSA P-256, because the schema names that curve. What crosses the wire is a public key and a signature over it, which is what makes a key lost here unrecoverable and a re-registration the remedy.

**The request's own signature is verified here before anything is sent** — the same `CheckSignature` `garam` runs, over the request parsed back from its PEM. The token is spent by the attempt rather than by the certificate, so a request `garam` would refuse costs the token and puts a person back in the console. The check sits immediately before the send, because that is what it guards.

**Nothing retries.** One token is one attempt, whatever it meets: a refusal, a listener that cannot be reached, or a store that would not take the answer. A second call presents a token that is already gone and reports a state that has already moved. The refusal is reported as one thing — the token is not usable, register again — and nothing here infers which of the three causes it was.

**The call is made over TLS verified against the root the deployment supplied, and never against the root the answer carries.** `--garam-trust-file` is what verifies the listener, as it does for every other call this operator makes; the `serverRootPem` in the answer cannot be what authenticates the answer that carried it. An enrollment made without verifying `garam` hands the token to whoever answered, which is the one way this design can be used wrongly, and it is the whole reason the enrolling connection presents no certificate but still verifies one.

**The answer's issuer and server root are read by nothing here and are not stored.** What verifies `garam` is the deployment's to supply ([ADR 0007](0007-claim-definitions-from-a-poller.md)), and what rewrites that file is a question ADR 0008 left open and this does not answer. The certificate and the key it was signed for are what the handshake reads, and they are what is stored.

**The garam server root the answer carries is compared against the root that verified the call, and a difference is named in one line.** It is stored nowhere and acted on never — the paragraph above is unchanged — and it fails nothing: an operator whose `garam` has rotated still enrolls. What it is for is narrow. Nothing rewrites `--garam-trust-file`, so a root `garam` has moved on from goes on working until every handshake fails at once, and this answer is the one place the two roots are side by side. The comparison is by SHA-256 over each certificate's DER, the fingerprint `openssl x509 -fingerprint -sha256` prints, so a line naming one names what a reader can compute from the file. It is `Info` rather than `Error` because nothing failed, and an answer carrying no certificate is compared against nothing and says so at `V(1)`: a field this operator does not act on does not earn a louder line for being absent.

**The parse refuses only an answer with no certificate in it.** Every other route in this client checks each field it is promised; this one cannot, because an enrollment cannot be asked for twice. Discarding a certificate over a field this operator never reads would cost the certificate and the token together.

**The token arrives as a file, as a key of the Secret the credential lives in.** It reaches the process the way the certificate, the key and the trust root do — the kubelet writes it, and this operator reads it at the attempt rather than holding it. It is never logged, never put in a status, and written to nothing. `--garam-enrollment-token-file` names the path and is the base's, for the reason `configuration.md` gives every other path under that mount.

**A second `Runnable` performs it, beside the poller and the renewer.** Added to the manager it joins the leader-election group (`sigs.k8s.io/controller-runtime@v0.24.1:pkg/manager/runnable_group.go:99`), which is load bearing here rather than tidy: a token is spent by the first call that presents it, so two processes enrolling one identity leave one of them holding nothing and a person registering again.

**It waits for a token and stops at the first attempt.** An operator is deployed before it is registered, so the token is placed while the process is already running; waiting is what keeps that from needing a restart. Two conditions end the wait, and both are properties rather than conveniences:

- **An absent or empty token file is not an error.** The flag is the base's and reaches every deployment, so this is the state most of them are in, and such a deployment builds and behaves exactly as it did before this change.
- **Enrollment is attempted only where this operator can read no certificate.** One that holds a credential does not spend a token to replace what the renewer renews, and the check is made at each look rather than once, so a credential placed out of band while this waits ends the wait too.

**An operator that is enrolling is not failed at startup for holding no certificate.** `MutualTLS` reads the credential at construction so that a certificate this process cannot read fails it once rather than every poll; where enrollment is configured that read is not made, because a certificate that is absent until this operator obtains one is the state enrollment is for. Its calls fail at the handshake until the credential lands, which is what those failures say.

## Consequences

**Registering an operator and placing one Secret key is the whole of what a person does.** `garam issue-certificate` stays the recovery from a token nobody received, and `gitops#222` still covers the first operator: this is what makes every operator after it self-enrolling.

**A certificate this operator enrolled with reaches the handshake about a minute later.** The kubelet carries the write into the volume rather than the process writing it, so the poll and the renewal fail at the handshake until it lands. That is ADR 0008's lag met on the first credential rather than a new one, and nothing restarts.

**The spent token stays in the Secret and in the projected file.** `ReplaceCredential` writes a merge patch, so it replaces the certificate and the key and leaves every other key of that Secret alone. A spent token enrolls nothing and the gate above means a running operator never presents one twice, so it is inert rather than dangerous — but it is left there deliberately and not by oversight. Removing it needs no new permission: the namespaced Role's `patch` on that Secret is enough to null the key out in the same write. It was not done here, because the write that would do it is the one that must not fail, and a person who can place a Secret key can remove one.

**A restart inside that lag presents a token that is already spent.** The certificate and the token are projected from one Secret, so a process starting before the kubelet has carried the write sees the token it spent and no certificate, and enrolls again. `garam` answers the one refusal, this reports it as "register again", and nothing is damaged — but the report is wrong about the cause for the length of that window. What would close it is a durable record of the attempt, and the only one available is the Secret this operator deliberately cannot read.

**Every deployment this repository ships loses the startup certificate read.** The base sets the token's path and the credential Secret both, so the eager read in `MutualTLS` is made by no deployment built from it: an operator whose credential was never placed now starts and fails at the handshake on every poll, where before it exited once and said why. That is the state enrollment needs — the certificate is absent until this operator obtains one, and failing on its absence would refuse the case this change exists for — and what it costs is that a deployment that forgot the Secret hears it from the poll rather than at startup. A deployment that wants the old refusal has the flag to unset.

**The root comparison is a notice and not a mechanism.** It fires once per enrollment, which is once per operator, so it catches a root that had already moved when this operator enrolled and says nothing about one that moves afterwards. What would catch that is a decision about rewriting the trust file, which ADR 0008 left open and this does not take.

**A key this operator loses is not recoverable.** `garam` signed a key it never held, so a store that refuses the answer loses the certificate with it. That is reported loudly and nothing retries it, for the same reason ADR 0008 gives for a lost renewal.

**The enrolling connection is anonymous and verified.** It presents no certificate, which is what the route is for, and it verifies `garam` against the pinned root — the property that keeps the token from being handed to whoever answered. No path in this repository sets `InsecureSkipVerify`, and the enrollment path is the one that would have been tempted to.

Ruled out: **reading the token from the API rather than from a file.** It is the `--garam-credential-secret` shape, and the permission is not what rules it out: `config/rbac/role.yaml` is a ClusterRole granting `get`, `list` and `watch` on Secrets cluster-wide, and the namespaced Role ADR 0008 describes adds `patch` on one Secret beside it — a grant adds and never narrows, so the read this operator was said not to have is there. **The cache is what rules it out.** A cached client reading a Secret's data caches every Secret this manager watches, and that same cluster-wide `list` is what makes it possible; an uncached reader avoids that and adds a second way of reading one thing to a process that reads its credential as a file. A file needs neither, and it is how every other secret this process holds already arrives.

Ruled out with it: **spending the token at startup rather than waiting for one.** It is fewer moving parts and it makes an operator deployed before it is registered need a restart, which is the manual step this change exists to remove. **Exiting after a successful enrollment so the next start reads the credential**, which turns the one-minute lag into a crash loop that re-presents the spent token at every restart. And **storing the answer's server root into the trust file**, which would rotate what verifies `garam` off an answer that root has just authenticated — a decision ADR 0008 left open and one this change does not need to take.
