# ADR 0075: Hold the manager and the control service not ready while their garam authentication has failed for an hour

> Status: accepted
> Date: 2026-10-19

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

On aurora, garam refused every call the operator made for three days and nobody noticed (#380). The operator logged each refusal, and nothing on aurora collects logs. `dev` also counts them, in `garam_operator_refusals_total` and `garam_operator_token_exchange_failures_total` ([ADR 0072](0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md) decision 8). Nothing on aurora scrapes metrics either: #380 records that `prometheusrules.monitoring.coreos.com` and `servicemonitors` do not exist there. A signal that a person meets without a metrics stack has to be one Kubernetes carries itself (#381).

The certificate that expired on aurora is gone from `dev` (#356). What remains is the same class of failure under ADR 0072's tokens. Both binaries hold an `internal/machinetoken` `Source`, which behaves as follows:

- It exchanges a client assertion for a token that garam answers with `expires_in` 3600 (ADR 0072 §Context, `garam@9ba35c3`).
- It replaces the token once two thirds of its lifetime have passed, and presents the held token for as long as it lives.
- After a failed exchange it waits 1 second, doubling up to 5 minutes, or a 429's `Retry-After`.
- Its `Transport` re-exchanges once after a 401.

Before this change, the manager's `/readyz` and `/healthz` were both `healthz.Ping`. The control service's `/readyz` answered only while its store answered a ping.

## Decision

1. **The form is readiness.** A process whose garam authentication has failed for longer than the bound reports itself not ready. Nothing else changes.
   - **Manager.** Each token source adds a readyz check beside `readyz`: `garam-authentication` for garam's audience and `control-authentication` for the control service's.
   - **Control service.** `/readyz` answers 503 `garam authentication lost` after the store ping passes.
   - **Liveness.** It is unchanged in both binaries. A restart does not repair a refused credential, so a liveness failure would only add a restart loop.
2. **What counts.** Each `Source` keeps the run of failures since its last success, and records these outcomes:

   | Outcome | Counts as |
   |---|---|
   | An exchange that answered a token | success |
   | An exchange that failed, for any of ADR 0072's reasons | failure |
   | A call through its `Transport` answered 401, after the one re-exchange | failure |
   | A call answered any other status below 500 | success |
   | A call answered 5xx, or no answer at all | nothing |

   A 5xx or a missing answer says nothing about the token. A failure to obtain a token was already recorded by the exchange that failed.
3. **The bound is one hour**, `machinetoken.SustainedFailure`. The check fails while the run is longer than that. It is derived from garam's 3600-second lifetime (written L below):
   - **At least L/3, 20 minutes.** A source whose refreshes fail goes on presenting its held token for up to a third of a lifetime. A shorter bound would report a process whose calls still succeed.
   - **More than 2L/3 plus the keep interval, 40.5 minutes.** Decision 4 asks for a token every 30 seconds, and the source exchanges at the latest 2L/3 after the token it holds was minted. So any failure is followed by a new outcome inside the bound, even with no traffic. A single refused call followed by silence therefore never reports anything.
   - **Not transient.** One failed exchange is retried after 1 second, or after its `Retry-After`. A run that lasts an hour has been retried throughout, at least once every five and a half minutes after the backoff reaches its ceiling. One throttled exchange, or a single 503, ends at the next success.
   - **One lifetime.** #381 names this bound, and no token garam answers outlives it.

   The bound is a constant rather than the `expires_in` last answered. A source that never obtained a token has no lifetime to read, and that is the case of a key placed wrong at onboarding.
4. **Each source keeps exchanging on its own schedule.** `Source.Keep` asks for a token every 30 seconds. A held token that is not yet due is answered without contacting garam, and the backoff paces a failing one.
   - **Manager.** `Keep` runs as a leader-election runnable, beside the poller.
   - **Control service.** `Keep` runs for the life of the process. A control service out of its Service's endpoints receives no requests, so without `Keep` nothing would ever exchange again, and a recovery would never clear the check.
5. **It clears on the first success.** No hysteresis is kept, because the bound already absorbs flapping. A success during a loss is evidence that authentication works now.
6. **Nothing new carries a secret.**
   - The check's error names when the run began and the reason ADR 0072 already reports: an exchange failure's `Reason`, or that a call was answered 401. It names no key, assertion or token.
   - controller-runtime's readyz body withholds every reason (`sigs.k8s.io/controller-runtime@v0.24.1` `pkg/healthz/healthz.go:117-119`), and the control service's body is a fixed string.
7. **The metrics stay.** `garam_operator_token_exchange_failures_total{audience, reason}` and `garam_operator_refusals_total{runnable, kind}` keep their names and labels. They remain the signal an alert is written against wherever something scrapes. Readiness is the signal where nothing does.

## What a not-ready process does

**The manager:**
- **Leader election.** Readiness does not affect it.
  - The Lease is renewed by client-go's leader elector, which controller-runtime starts apart from the probe server (`pkg/manager/internal.go`: `initLeaderElector`, and the health probe server added as an HTTP server runnable).
  - A not-ready leader keeps the Lease and keeps running its runnables. That is how it sees the recovery.
  - A replica that is not the leader runs no `Keep` and nothing else that calls garam. It records no outcome and stays ready. A rollout during a loss therefore completes, and the new leader is reported once the bound passes.
- **Webhooks.** The manager serves none.
  - controller-runtime adds the webhook server only from `GetWebhookServer` (`pkg/manager/internal.go:279-289`), and nothing in this repository calls it.
  - `config/default/kustomization.yaml` leaves `../webhook` commented out.
- **The Service.** `config/default/metrics_service.yaml` selects the manager's Pod, and a Pod that is not ready leaves the Service's ready endpoints.
  - Nothing reaches the manager through that Service except a metrics scraper.
  - A deployment that scrapes through it confirms that its scraper reads endpoints that are not ready. Otherwise it loses the counters for exactly the length of the loss.
- **Restarts.** None. `/healthz` stays `healthz.Ping`, and the kubelet restarts a container on its liveness probe only.

**The control service:**
- **Its Service** stops routing to it. Every route it serves proves its caller at garam, so each one would fail during the loss anyway. That is the trade `/readyz` already makes for the store.
- **Leader election and webhooks.** It runs neither.
- **The manager** finds a not-ready control service unreachable. That records nothing on the manager's control source, so the manager is not reported for the control service's loss.
- **Restarts.** None. `/healthz` answers 200 unconditionally.

## Consequences

- **What a person sees.** `kubectl get pods` shows `READY 0/1` on the process that lost its authentication, and `kubectl get deploy` shows it unavailable. `kubectl get events` carries the kubelet's `Unhealthy` Warning, `Readiness probe failed`. All of this clears at the next probe after a success.
- **The detection delay.** It is up to one hour past the last success, against the three days on aurora.
- **The control service's probe is the deployment's.** Its manifests are not in this repository. Wherever it runs, the deployment probes `/readyz` for readiness and nothing else for liveness.
- **One case is not reported.** In that case garam answers every exchange with a token and refuses every call presenting it with 401. Each call's re-exchange is a success that ends the run. The manager still counts the refusals in `garam_operator_refusals_total{kind="401"}` and logs them.
- **The control service has no metrics endpoint.** For it, readiness and its log are the whole signal.

## Rejected alternatives

- **A Warning Event.**
  - The control service holds no Kubernetes client and no RBAC on any object. Giving it both adds a credential to a service that otherwise needs none.
  - An Event expires after the API server's `--event-ttl` (default 1 hour), so a loss of a day needs a re-emitter that keeps state.
  - Readiness gives `kubectl get events` the kubelet's `Unhealthy` Warning without either.
- **A condition on the `Agent`s the manager constructs.** It reaches only the manager, so a control service's loss shows nowhere. An operator that holds no agents yet, which is the onboarding case, shows nothing. It would also add a second writer to `Agent` status, which `stack-kubebuilder.md`'s rule "One reconciler per kind" reserves to one reconciler.
- **A liveness failure.** A restart presents the same key, so it repairs nothing, and #381 excludes it.
- **A second run, for 401s alone, that only an accepted call ends.** It would catch the case the Consequences leave out. But a control service that is not ready receives no calls, so on that binary the run would never end.
- **A shorter bound.** Below 40.5 minutes, a single refused call followed by silence raises the check before `Keep`'s next exchange can answer it. Below 20 minutes, a source riding a held token through failed refreshes is reported while its calls succeed.
