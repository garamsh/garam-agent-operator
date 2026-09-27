# ADR 0022: End the enrollment on a certificate that has not expired, rather than on one this operator can read

> Status: accepted
> Date: 2026-09-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0020](0020-enroll-this-operator-with-a-one-time-token-and-keep-the-key-it-generated.md) decided when this operator enrolls, in one clause: "enrollment is attempted only where this operator can **read** no certificate." [ADR 0021](0021-present-any-one-enrollment-token-once-and-wait-for-another.md) carried that clause forward in the same words. The reason both give is sound and is not in question — an operator holding a credential would be spending a token to replace what the [Renewer](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md) renews.

**What the clause conflates is reading a file with holding an identity.** The guard is `tls.LoadX509KeyPair`, which parses the pair and checks that the key matches the certificate. It reads neither `notBefore` nor `notAfter`, because nothing asked it to. So a certificate that expired loads without error, the guard reports a certificate held, and the enrollment ends before the token file is read at all. The code is a faithful implementation of the decision; the decision is what is wrong.

**Both recovery routes are then closed, and they close each other.** A renewal authenticates with the certificate it is renewing, so an expired one is refused. An enrollment runs only where no certificate can be read, and one can be read. An operator whose certificate lapses before it is renewed recovers by no route this project publishes.

Measured on `admin@garam-dev` on 2026-09-05 and recorded in issue #140, against `dev` at `2285768`: the mounted certificate was `grn:garam:default:operator:gagent-dev` with `notAfter Sep 4 09:56:52 2026`, expired the day before. The operator restarted at 09:42:40, logged "Enrolling nothing: this operator holds a certificate already" at 09:43:09, and its next line was a renewal refused `401 unauthenticated: no machine identity`. A token placed in the Secret would have been read by nothing.

**This is not what ADR 0021 decided.** That decision made a *refused* token recoverable without a restart. This ends before any token is read, so a replacement token reaches the same guard and stops at it.

**The axis is one this project has met before.** `Synced=True` against a workload that never started (#72), an image in the registry counted as an image in the cluster (#124), and here a certificate on disk counted as an identity: a thing that exists counted as a thing that works.

**What `garam` does with such an enrollment is read and not observed.** Reported by `garamsh/garam`'s PM on 2026-09-05 and recorded in #140: `ca.service.Sign` reaches `authorityOf`, which treats a missing authority as "not yet" and creates one, sealing it with the key-encryption key the running `machine` Pod holds. Lazy creation on the first enrollment is the design. They were explicit that they read the path in source rather than observed it, and nobody has reached it — because this guard is the reason nobody ever has.

## Decision

**Enrollment is attempted where this operator can read no certificate that has not expired.** ADR 0020's clause is narrowed by one word and its reason is unchanged: what must not be replaced by a token is a credential that still authenticates, and that is what the guard now tests for rather than approximating by whether a file parses.

**Expired means past `notAfter` on this operator's own clock, and no margin is allowed for skew.** The two errors are not symmetric, which is what decides it. A margin enrolls while `garam` would still admit the certificate, which spends a token to replace a working credential — the exact loss ADR 0020's clause exists to prevent. No margin, at worst, delays the enrollment by the skew, and the loop looks again ten seconds later. A margin would also need a number, and there is no clock source here to derive one from; inventing one silently is worse than not having one.

**`notBefore` is not read.** A certificate whose validity has not started is one that is about to work, and waiting resolves it; enrolling against one would spend a token to replace a credential arriving under its own power. Only a certificate past `notAfter` is unambiguously the enroller's — everything before that instant is the renewer's.

**The pair is still loaded the way the handshake loads it.** A certificate stored beside a key it was not signed for is no certificate here either. What changes is what is read off a pair that loads, not whether it must load.

**ADR 0020's guarantee is unchanged, and it is what bounds this narrowing.** "Spends nothing where this operator holds a certificate" becomes "spends nothing where this operator holds a certificate that has not expired". A restart still cannot burn a token to replace a live credential, because a live credential is now precisely what the guard tests for. What a restart can no longer do is refuse to enroll while holding a certificate that authenticates nothing.

**The enroller replaces an expired credential through the store, as it writes any other.** Nothing new is decided here: `ReplaceCredential` writes the certificate and the key together, which is what a certificate signed over a freshly generated key requires. The expired pair is worth nothing on its own — the key beside it renews nothing, because the renewal it would authenticate is the one being refused.

**A certificate found expired is reported at `V(1)`, and that this operator is enrolling is reported at `Info`.** The expiry line is emitted at every look, which is the level an absent token file is already read at. What says enrollment is under way is the line `Start` emits once — and in this state it is now emitted at all, which is what the measured operator never said.

**Everything else ADR 0021 decided is carried forward unchanged.** It is restated here rather than left in a superseded record, for the reason [ADR 0017](0017-an-unreconciled-environments-values-live-in-an-overlay-here.md) gives: Rule 2 freezes an ADR as a unit, and a reader should not have to open a superseded ADR to find a live rule. The reasoning behind each stays in the ADR that took it.

- A token is presented once and never twice, and what ends the enrollment is a certificate rather than an attempt.
- What a token was is remembered as its SHA-256 digest, in the process and nowhere else.
- The record is made at the call that presents the token, not at what that call answers; the one failure that records nothing is a key this operator could not generate.
- The refusal says that another token placed in this file is presented without restarting this operator, and names no minting route.
- This operator obtains its first certificate itself, by presenting a token a person minted by registering it, and writes what comes back through the store ADR 0008 decided.
- The key pair is generated in the cluster, ECDSA P-256, and the private half never leaves it.
- The request's own signature is verified here, immediately before anything is sent.
- The call is made over TLS verified against the root the deployment supplied, and never against the root the answer carries.
- The answer's issuer and server root are read by nothing here and are stored nowhere.
- The `garam` server root the answer carries is compared against the root that verified the call, by SHA-256 over each certificate's DER, and a difference is named in one line that fails nothing.
- The parse refuses only an answer with no certificate in it.
- The token arrives as a file, as a key of the Secret the credential lives in, named by `--garam-enrollment-token-file`.
- A second `Runnable` performs the enrollment, beside the poller and the renewer, and joins the leader-election group so that two processes never spend one identity's token.
- An absent or empty token file is not an error.
- An operator that is enrolling is not failed at startup for holding no certificate.

## Consequences

**An operator whose credential lapsed recovers by the route this project already publishes.** A person places a token in the Secret key, and the operator presents it on its next look — the same act ADR 0021 made the recovery from a refused token. What the issue described as the workaround, deleting `certificate.pem` and `key.pem` from the Secret, is a destructive edit no document here ever asked for, and it is now needed for nothing.

**This makes the experiment possible and does not settle it.** Whether the enrollment then succeeds rests on `garam` creating a certificate authority lazily during it, which #140 records as read in source and never observed. `garam`'s PM has committed to measuring it; `certificate_authorities` moving from no rows to one during the first enrollment is what would confirm the reading. Merging this restores no cluster on its own.

**A clock running ahead of `garam`'s can still spend a token early.** Refusing a margin means this decision adds nothing to that risk, but it does not remove it: an operator whose clock is far enough ahead reads a live certificate as expired and enrolls. The cost is one token and one registration, the same cost ADR 0020 accepted for a request `garam` would refuse, and nothing here has a second clock to check against.

**The renewer goes on failing at 401 while the enrollment waits.** Nothing here stops it, and ADR 0008's reasoning is untouched: the poll and the renewal failing loudly are the only signal that something is wrong, and an operator that quieted them would say nothing at all.

**A credential placed out of band still ends the wait, if it has not expired.** ADR 0020's property is narrowed the same way the guard is, and an expired credential placed by hand leaves the operator waiting — which is the state it is in.

**The window between expiry and the operator noticing is one look.** The guard is read at each attempt, so an operator running when its certificate expires falls through within ten seconds and waits for a token from there.

**The check set keeps the live-certificate case as a control rather than as a test of its own.** `testing.md` §Failing requires a refusal to be asserted beside the case that stops being accepted without it, and after this change that case exists: the two differ in `notAfter` alone and are read together.

Ruled out: **a clock skew margin**, for the asymmetry above — the margin's failure spends a token against a working credential and its absence costs a bounded delay.

Ruled out with it: **reading the expiry inside `operatorCertificate`**, which is also what the handshake presents and what `MutualTLS` reads at startup. Refusing an expired pair there would make the poller present no certificate where it now presents a stale one, and would fail an operator at startup over a credential it is about to replace. Both are decisions this change does not need and did not examine. And **treating a certificate inside its renewal window as the enroller's**, which is the renewer's by ADR 0008 and would spend a token on a credential that still authenticates.
