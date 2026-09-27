# ADR 0030: Carry the operator's certificate expiry and garam's refusals as metrics, under names an alert can depend on

> Status: accepted
> Date: 2026-09-28

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0008](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md) decided that an unrenewed certificate stops nothing: the poll goes on failing and saying why, and the renewer says which refusal it met, because a poller stopped instead would remove the only signal. That reasoning is not in question here. What it leaves open is where the signal goes, and the answer so far is a log line. [ADR 0007](0007-claim-definitions-from-a-poller.md) names "metrics for the poll" among what it does not decide.

Issue #137 records what that cost on `admin@garam-dev`, from 32,843 log lines of image `da07a03e1d20`:

- The poller began failing `remote error: tls: bad certificate` at 2026-09-03 22:30:21 and the renewer at 23:14:13, hourly, eleven times before the certificate's `notAfter` of 2026-09-04 09:56:52.
- Each renewer failure was one `ERROR` line among 2,147 poller errors saying much the same thing. No condition, metric or event carried it.

Two later comments on the issue narrow what can carry it:

- **No local check sees this failure.** The organisation was deleted and recreated under the same GRN, and `certificate_authorities` held no row from then until this operator enrolled again on 2026-09-06. The mounted `issuer.pem` was the old issuer, valid to 2036; the leaf chained to it and had eleven hours to run. Expiry, chain verification and issuer against server root all passed throughout. The only thing that knew was `garam`'s refusal, which the operator received and spent on a log line.
- **The renewer was not the failing component.** No renewal could have succeeded with no authority to sign it. What failed was that nothing listened, so what is decided here is a place for the signal and not a change to the renewer.

Three places were weighed on the issue:

- **A metric.** The manager already serves one endpoint, through the registry controller-runtime owns (`stack-kubebuilder.md` §8), and an alert rule is what reads one.
- **The health probe.** A failed probe restarts the Pod, and a restart recovers neither failure recorded here: the credential is the same after one, and the missing authority was not in this cluster.
- **An Event** on the Deployment or the Secret.

The PM's decision on the issue, 2026-09-27, takes the metric and neither of the others.

## Decision

**This operator exports two metrics through `metrics.Registry`, on the endpoint the manager already serves.**

- `garam_operator_certificate_not_after_timestamp_seconds`, a gauge with no labels: the `notAfter` of the certificate this operator last read to authenticate to `garam` with, in Unix seconds. It is set wherever the pair is loaded — at startup, at every handshake, and at each look the enroller takes — so it follows a renewal the next time a handshake reads the file. It exports no series until a certificate has been read, because a zero would read as a certificate that expired in 1970.
- `garam_operator_refusals_total`, a counter labelled `runnable` and `kind`: every refusal `garam` answered this operator. `runnable` is the one that met it — `poller`, `renewer`, `reporter` or `enroller`. `kind` is `handshake` where `garam` refused at the handshake with an alert, and the HTTP status as a decimal where it answered one.

**What is counted is `garam` refusing, not this operator failing.** A handshake refusal is the alert `garam` sent, which `crypto/tls` reports as a `net.OpError` whose `Op` is `remote error`. A listener this operator did not verify, one it did not reach, a certificate it could not read, and an answer it could not decode are its own failures and are not counted.

**A renewal refused as too early is not counted.** ADR 0008 already decides it is not a failure. It is the answer for two thirds of every certificate's life, and it shares status 409 with "superseded", so counting it would make the renewer's `409` series rise on every interval of a healthy operator. That would leave the one refusal the renewer needs to report with no series of its own.

**The two names and their labels are a stable surface.** They are what an alert rule is written against, wherever that rule lives. Renaming either, or changing what a label value means, is a new decision that supersedes this one, not an edit.

**Nothing stops.** ADR 0008's reasoning is untouched: the poller and the renewer go on failing and logging as they did. The metrics are a second place for the same signal, not a replacement for it.

## Consequences

**An alert can see the failure of 2026-09-03 as it starts.** On that timeline `increase(garam_operator_refusals_total{kind="handshake"}[1h])` would have risen from the poller's first refused handshake at 22:30:21, eleven and a half hours before the certificate expired. That is the whole of the value on offer: the operator could not have repaired the missing authority, and knowing sooner is what shortens the outage.

**Two failures that look identical to this operator still look identical.** `garam` answers "the authority that signed me is gone" and "my token is wrong" as a handshake failure or a bare 401, so these metrics carry the kind `garam` answered and not the cause behind it. `garam`'s PM is opening an issue to separate the two refusals. When that lands, the kind label carries the distinction without a new metric.

**The gauge alone does not catch this failure class, and it is not meant to.** It reports expiry, which the 2026-09-05 comment shows passes straight through a deleted authority. It is what an alert on an ordinary lapse reads — a renewer that stopped being admitted for any reason — and the counter is what carries the rest.

**The client keeps the refusal beside the sentinel it maps it to.** `ErrClaimConflict`, `ErrAgentNotHeld`, `ErrReportStale`, `ErrRenewalTooEarly`, `ErrCredentialSuperseded` and `ErrTokenNotUsable` each wrap the refusal as well, so the status reaches the counter through every error a method answers. Their messages now carry `garam`'s status and message after the sentinel's.

**`github.com/prometheus/client_golang` becomes a direct dependency.** It was already in the module through controller-runtime, at the same version.

Ruled out: **failing the health probe**, because a restart recovers nothing in either failure recorded on #137 and takes the manager's reconcilers down with it. **An Event**, not in this change: nothing here needs one yet, and the metric is what an alert reads. **Deriving validity locally** from more of the certificate, because every local check passed through the recorded failure. **Labelling by `garam`'s error kind** rather than its status, because the status is the bounded set, and the too-early answer — the one case where the kind is needed — is the one not counted.
