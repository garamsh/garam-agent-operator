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

garam `28754663e27e20dd43c9451d53829ba112ed23ad` publishes two facts this decision rests on, in `api/machine.yaml`:

- **`getIdentity`** (`GET /identity`, lines 185-202) accepts `machineBearer` and answers the caller's identity, or 401. It takes no contract header and changes nothing.
- **What one call costs.** The listener reads a token's row, and what it rests on, on every request (lines 19-23). An exchange is bounded at 10 verified ones per `client_id` per minute (lines 377-381), and that budget is shared with every other exchange the operator sends.

The failure #380 recorded is garam answering 401 `no machine identity` to every call. Under tokens, the same failure has garam answering every exchange with a token and every call presenting it with 401. The `Transport`'s re-exchange after each 401 then succeeds every time.

Before this change, the manager's `/readyz` and `/healthz` were both `healthz.Ping`. The control service's `/readyz` answered only while its store answered a ping.

## Decision

1. **The form is readiness.** A process whose garam authentication has failed for longer than the bound reports itself not ready. Nothing else changes.
   - **Manager.** Each token source adds a readyz check beside `readyz`: `garam-authentication` for garam's audience and `control-authentication` for the control service's.
   - **Control service.** `/readyz` answers 503 `garam authentication lost` after the store ping passes.
   - **Liveness.** It is unchanged in both binaries. A restart does not repair a refused credential, so a liveness failure would only add a restart loop.
2. **What counts.** Each `Source` keeps one run of failures. The run records its cause, and the cause decides what ends it:

   | Outcome | Opens or extends a run | Ends a run |
   |---|---|---|
   | An exchange that failed, for any of ADR 0072's reasons | yes, caused by an exchange | — |
   | A call through its `Transport` answered 401, after the one re-exchange | yes, caused by a call | — |
   | An exchange that answered a token | — | only a run caused by an exchange |
   | A call answered any other status below 500 | — | any run |
   | A call answered 5xx, or no answer at all | — | — |

   - **A run caused by a call** is ended only by an accepted call. The exchange that follows a 401 answers a token whether or not garam accepts it, so its success says nothing about the refusal.
   - **A run caused by an exchange** that then meets a refused call becomes a run caused by a call. It keeps the time it began.
   - **A 5xx or a missing answer** says nothing about the token.
   - **A failure to obtain a token** was already recorded by the exchange that failed.
3. **The bound is one hour**, `machinetoken.SustainedFailure`. The check fails while the run is longer than that. It is derived from garam's 3600-second lifetime (written L below):
   - **At least L/3, 20 minutes.** A source whose refreshes fail goes on presenting its held token for up to a third of a lifetime. A shorter bound would report a process whose calls still succeed.
   - **More than the longest gap between two outcomes, 5.5 minutes.** On a source whose audience is garam, decision 4 calls `getIdentity` every 5 minutes and asks for a token every 30 seconds, with or without traffic. A garam that answers meets the identity call with an outcome. A failing exchange is retried at least once per 5-minute backoff ceiling plus one 30-second tick. So a run that has lasted the bound has met about a dozen outcomes, and a single refused call or failed exchange is followed by another outcome long before the bound.
   - **Not transient.** One failed exchange is retried after 1 second, or after its `Retry-After`. A run that lasts an hour has been retried throughout, at least once every five and a half minutes after the backoff reaches its ceiling. One throttled exchange, or a single 503, ends at the next success.
   - **One lifetime.** #381 names this bound, and no token garam answers outlives it.

   The bound is a constant rather than the `expires_in` last answered. A source that never obtained a token has no lifetime to read, and that is the case of a key placed wrong at onboarding.
4. **Each source keeps itself observed.** `Source.Keep` asks for a token every 30 seconds. A held token that is not yet due is answered without contacting garam, and the backoff paces a failing one.
   - **The identity call.** On a source whose audience is garam, `Keep` also sends `GET /identity` through the source's own `Transport`: once when it starts, then every 5 minutes. It reads nothing from the answer. Its outcome reaches the run as any call's does, so a source nothing else calls through still meets a call's outcome. That is what ends a run caused by a call on an idle source.
   - **Why 5 minutes.** One call every 5 minutes is 12 requests an hour per source. Each costs garam the per-request token read it already pays for every call the operator makes. Where garam refuses the token, the `Transport`'s re-exchange spends at most one of the operator's 10 verified exchanges a minute per identity call. The upper side is set by the bound, because the interval must stay far inside it (decision 3). It is also the longest a control service that is out of its Service's endpoints waits after a recovery before its next probe passes. A shorter interval adds load to every garam without moving the detection, which the bound fixes at an hour.
   - **Manager.** `Keep` runs for each source as a leader-election runnable, beside the poller. Only the `garam` source makes the identity call.
   - **The manager's `control` source** makes no identity call: its token is shown only to the control service, and the control service proves each request at garam (ADR 0072 decision 3). A run on that source is ended by the next accepted call to the control service. The desired-state puller sends those calls whatever the manager's readiness. It asks again at the latest 5 minutes after a refusal (`internal/desired/puller.go` `Start`, `agent.md`), so the gap between two outcomes on this source is no longer than on a `garam` one, and the source needs no call of its own.
   - **Control service.** `Keep` runs for the life of the process, with the identity call. A control service out of its Service's endpoints receives no requests, so without `Keep` nothing would exchange or call again, and a recovery would never clear the check.
5. **It clears on the first success that ends the run** (decision 2). No hysteresis is kept, because the bound already absorbs flapping. Such a success during a loss is evidence that authentication works now.
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
- **The detection delay.** It is up to one hour past the last success, against the three days on aurora. A recovery clears the check at the next outcome that ends the run: at most 5 minutes later on an idle `garam` source, and sooner where traffic calls garam first.
- **The control service's probe is the deployment's.** Its manifests are not in this repository. Wherever it runs, the deployment probes `/readyz` for readiness and nothing else for liveness.
- **#380's signature is reported.** garam answering every exchange with a token and every call with 401 opens a run caused by a call. The identity call keeps that run growing with no other traffic, and the check fails once it has lasted the bound.
- **A refused manager whose control service then goes silent stays reported.** A run on the manager's `control` source ends only at an accepted call to the control service. Unreachable answers record nothing, so a run opened by a 401 goes on growing while the control service answers nothing at all, and it ends at the first call the control service accepts.
- **Every garam source costs garam one more request every 5 minutes.**
- **The control service has no metrics endpoint.** For it, readiness and its log are the whole signal.

## Rejected alternatives

- **A Warning Event.**
  - The control service holds no Kubernetes client and no RBAC on any object. Giving it both adds a credential to a service that otherwise needs none.
  - An Event expires after the API server's `--event-ttl` (default 1 hour), so a loss of a day needs a re-emitter that keeps state.
  - Readiness gives `kubectl get events` the kubelet's `Unhealthy` Warning without either.
- **A condition on the `Agent`s the manager constructs.** It reaches only the manager, so a control service's loss shows nowhere. An operator that holds no agents yet, which is the onboarding case, shows nothing. It would also add a second writer to `Agent` status, which `stack-kubebuilder.md`'s rule "One reconciler per kind" reserves to one reconciler.
- **A liveness failure.** A restart presents the same key, so it repairs nothing, and #381 excludes it.
- **Ending every run at an exchange that answers a token.** Each re-exchange after a 401 would end the run, so garam answering every exchange with a token and every call with 401, the failure #380 records, would never be reported.
- **A call-caused run with no call of the source's own.** A control service that is not ready receives no calls, so on that binary the run would never end. The identity call is what lets decision 2 hold on both binaries.
- **Calling `getIdentity` at every 30-second keep tick.** It is ten times the load on every garam and moves neither the detection nor the bound.
- **An identity call on the manager's `control` source.** No route of the control service is a call that costs nothing: each request is proved at garam. The manager's own calls to it already supply the outcomes.
- **A shorter bound.** Below 20 minutes, a source riding a held token through failed refreshes is reported while its calls succeed. Nearer the 5.5-minute gap, a single refused call could be reported before the next outcome answers it.
