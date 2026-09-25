# ADR 0021: Present any one enrollment token once and wait for another, and end the enrollment on a certificate rather than on an attempt

> Status: superseded by ADR-0022
> Date: 2026-09-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0020](0020-enroll-this-operator-with-a-one-time-token-and-keep-the-key-it-generated.md) decided how this operator obtains its first certificate, and two of its decisions are about what happens after a token fails. The first is **"Nothing retries. One token is one attempt, whatever it meets"**, which is a fact of `garam`'s contract read at the specification: a token is spent by the call that presents it whether or not that call goes on to succeed. The second is **"It waits for a token and stops at the first attempt"**, which is not a fact of the contract but a step taken from it, and it is the one below.

That step held while a refused token was the end of the road. ADR 0020 says a refused token "is one to register again for", and registering again was a person's act this operator could not observe: it minted a token under another identifier, and reaching a `Runnable` that had already returned needed the Pod restarted. Stopping cost nothing it could see, because there was nothing further for it to see.

**`garam` has decided there is.** `garamsh/garam#1027`, closed as completed on 2026-09-05, decides that a signed-in member may mint a second enrollment token for an operator that holds no certificate, and requires that the old token stops working when they do. Re-minting for an operator that already enrolled stays undecided and the route refuses it. That turns the person this operator waits on into someone who can hand it another token.

**The route is decided and not built.** Searched on 2026-09-05, no issue and no pull request in `garamsh/garam` references #1027. What has changed is the decision, and what this repository can rest on is the shape of the token rather than the existence of a console button.

Three facts about this repository bound what the change costs, and each is why the gap is the loop rather than anything around it.

- **A replacement token already reaches the running container.** `config/manager/manager.yaml` mounts the credential Secret whole, and no `subPath` appears anywhere under `config/`, so the kubelet carries a changed key into the volume this operator reads. The manifest says so where the volume is declared.
- **The token is read at the attempt rather than held.** `internal/garam/enroller.go` reads the path each time it looks, so a key that changed under it is the key it reads next.
- **A restart is not a reliable recovery either.** `internal/garam/credentialstore` replaces the credential with a merge patch, which leaves every other key of that Secret alone, so the spent `enrollment-token` survives the write. A restarted operator re-presents the same dead token, is refused, and stops again. Recovery today is two manual acts — replace the key, then restart the Pod — and the second exists only because the first cannot be noticed.

**One fact ADR 0020 stated is wrong, and it is what made stopping look forced.** Its consequence "A restart inside that lag presents a token that is already spent" ends: "What would close it is a durable record of the attempt, and the only one available is the Secret this operator deliberately cannot read." The record it names is not the only one available. A process holds a record of what it has presented for its own lifetime, and that lifetime is exactly the scope over which a loop would present anything twice. Such a record does not survive a restart and does not need to: a restart re-reads the certificate first, and the case it cannot tell apart is the one ADR 0020 already named and left standing.

## Decision

**A token is presented once and never twice, and what ends the enrollment is a certificate rather than an attempt.** ADR 0020's "Nothing retries" is carried forward and narrowed: it bound one attempt to one process, and it now binds one attempt to one token. Presenting the same token again can only fail while reading as though the state were still open, which is ADR 0020's reason and is unchanged; it says nothing about a token this operator has not presented. A refused token therefore leaves the `Runnable` looking at the same file on the same interval, and the two conditions ADR 0020 gave for ending the wait become one — a certificate this operator can read, whether it enrolled for it or found it.

**What a token was is remembered as its SHA-256 digest, in the process and nowhere else.** Digests rather than tokens, because nothing here holds a token past the call that presents it, and ADR 0020's rule that a token is written to nothing is what that rule protects. It is not put in the Secret, in a status or in a log line. It is held for the life of the process, which is the whole of the span in which this operator could present one twice.

**The record is made at the call that presents the token, not at what that call answers.** `garam` spends a token on the attempt, so a token presented here is gone whether or not an answer came back, and a failure between the send and the answer must not leave this operator believing the token is still good. Every way an enrollment can fail after the request is built therefore records the token as spent; the one failure that records nothing is a key this operator could not generate, where nothing was presented and the token stands.

**The refusal says what this operator does with a replacement, and not where one comes from.** ADR 0020's line said "register this operator again", which named the only route there was and needed a restart it never mentioned — a reader could do everything it asked and watch nothing happen. The line now says that another token placed in this file is presented without restarting this operator. It names no minting route, because #1027's is decided and not built, and a line promising it would be wrong for however long that lasts.

**Everything else ADR 0020 decided is carried forward unchanged.** It is restated here rather than left in a superseded record, for the reason [ADR 0017](0017-an-unreconciled-environments-values-live-in-an-overlay-here.md) gives: Rule 2 freezes an ADR as a unit, and a reader should not have to open a superseded ADR to find a live rule. The reasoning behind each stays in ADR 0020, which is the record of why they were taken.

- This operator obtains its first certificate itself, by presenting a token a person minted by registering it, and writes what comes back through the store [ADR 0008](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md) decided.
- The key pair is generated in the cluster, ECDSA P-256, and the private half never leaves it.
- The request's own signature is verified here, immediately before anything is sent.
- The call is made over TLS verified against the root the deployment supplied, and never against the root the answer carries.
- The answer's issuer and server root are read by nothing here and are stored nowhere.
- The `garam` server root the answer carries is compared against the root that verified the call, by SHA-256 over each certificate's DER, and a difference is named in one line that fails nothing.
- The parse refuses only an answer with no certificate in it.
- The token arrives as a file, as a key of the Secret the credential lives in, named by `--garam-enrollment-token-file`.
- A second `Runnable` performs the enrollment, beside the poller and the renewer, and joins the leader-election group so that two processes never spend one identity's token.
- An absent or empty token file is not an error, and enrollment is attempted only where this operator can read no certificate.
- An operator that is enrolling is not failed at startup for holding no certificate.

## Consequences

**Recovery from a refused token is one act.** A person places another token in the Secret key, and the operator presents it on its next look. The Pod restart that ADR 0020's recovery needed is gone, which is the whole of what this decides.

**The spent token stays in the Secret, as ADR 0020 left it.** Erasing it is a write to a key this operator was given rather than one it owns, and this decision does not need it: a token this process has presented is passed over by the digest record, and one it has not presented is the one it is waiting for. Whether a consumer should erase it is a separate question and is still open.

**The record does not survive a restart, and the window ADR 0020 named is narrowed rather than closed.** A process starting inside the lag between a stored credential and the kubelet carrying it into the volume still sees a spent token and no certificate, and still presents it once. What is different is what happens next: it now waits rather than returning, so the cost of that window is one refusal line that is wrong about the cause, and no longer a `Runnable` that has ended holding nothing.

**An operator that is never given a token holds a `Runnable` open for the life of the process.** That was already true of ADR 0020's wait and is unchanged; what changes is that a refusal no longer ends it either. Nothing else this operator does depends on that `Runnable` returning, which is why `Start` answers no error.

**Every refusal this operator can meet now reads as recoverable, because it is.** ADR 0020 reported three of them — a token `garam` would not take, a call that failed, and a store that dropped the answer — as states a person recovers from by registering again. They are now reported as states another token recovers from, which is a different instruction to the same reader and the reason the wording is decided here rather than left to the code.

**A test of this waits on the real interval.** The `Enroller` takes its interval from a package constant, where the poller, the renewer and the reporter each take theirs as a constructor argument, so a test that must see a second look at the token file waits the whole of it. Nothing here changes that, and it is the one cost this decision adds to the check set.

Ruled out: **reading the spent token back out of the Secret so that the record survives a restart.** ADR 0020 ruled out reading that Secret through the API and the reason it gave is unchanged — a cached client reading a Secret's data caches every Secret this manager watches. The restart window is narrow, it is bounded by the kubelet's carry, and it now costs a wrong log line rather than a stopped enrollment; buying it back with a cluster-wide cache of Secret data is the wrong trade.

Ruled out with it: **remembering the token rather than its digest**, which would hold a secret in memory for the life of the process to answer a question a digest answers exactly as well. And **ending the enrollment when a token is refused but leaving the `Runnable` registered**, which is what a `Runnable` that returns already is: the manager holds no second chance to give it, so anything short of the loop is the behaviour this replaces.
