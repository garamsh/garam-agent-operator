# ADR 0016: Report to garam what this operator observed, stay silent where it observed nothing, and carry the epoch on the Agent it was proved at

> Status: accepted
> Date: 2026-09-02

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

`garam` published a route for the thing this operator knows and has had nowhere to say. At `garam@e1e69fd`, `api/machine.yaml:201` serves `POST /agents/{agent}/provisioning-state` under `operationId: reportProvisioningState`, and `api/machine.yaml:605-610` gives `ProvisioningState` the enum `[pending, provisioned, ready, failed]` with the note that "`pending` is an agent no operator has reported on yet". Until it is called, an agent this operator provisioned stays at `pending` forever and `garam` cannot tell an agent that never started from one that is running.

Three sentences of the route's own description are the design constraints rather than colour.

- It carries the epoch the operator holds the agent at, and `garam` accepts it only at the epoch the assignment is currently on. A stale one is refused under `failed_precondition`.
- An operator the agent is not assigned to is refused, and an agent that does not exist is refused the same way, so a caller learns nothing about the agents it does not hold.
- `garam` keeps the last report and nothing before it, and expires none of them, so "a state stands until it is reported again — a report is evidence of what the operator saw, never evidence that the operator is still there."

The third is a latch, not a heartbeat, and it is what most of this decision turns on.

**What this repository held.** The epoch was obtained and discarded on the same line. `internal/garam/client.go` decoded a definition's claim into `Claimed: p.Claim != nil`, throwing the epoch away, and `internal/garam/poller.go` logged the epoch its own claim answered and returned a `bool`. `grep -rniE 'epoch' internal/controller/ api/ config/` returned nothing on `f642f21`, so no value the route requires reached a reconcile.

**What this operator observes.** `StatefulSet.status.readyReplicas`, and nothing else. `agent.md` already records what that is worth: the `Available` condition's message says a ready replica means the containers are running and "nothing about whether the agent inside them works", and its `False` message says no ready replica "covers a replica still starting as much as one that cannot start, and does not say which". The cluster proves the first half — the one agent reading green runs nginx.

**What is not available.** A definition is claimed once, so `claimDefinition` answers `409` forever after the first claim and the epoch cannot be re-obtained from it. `listDefinitions` does carry `claim.epoch`, but `api/machine.yaml:322-330` says what it is: "A claim here is not evidence of holding... the definition stays listed, stays claimed, and carries the epoch of whoever holds the agent by then." It reports the assignment's epoch, not this operator's.

`agent.md` carried "What this operator reports back to `garam`" as an open question. This closes it.

## Decision

**This operator reports what it observed and nothing else. Where it observed nothing, it sends nothing rather than a state standing in for one.**

### The epoch lives in `AgentStatus`, written where it was proved and never refreshed

`status.epoch` on the `Agent`, beside `status.agent`. It is written on the construct path and read everywhere else.

It is Status and not Spec for the reason `agent.md` already gave the GRN: neither is something a user writes, and `garam` is where a claim is durable. The two are halves of one answer — `claimDefinition` returns `AgentAssignment{grn, operator, epoch}` — and splitting them across two homes under two rules would be the odd choice. That an epoch is what `garam` answered rather than what the operator saw in the cluster does not make it desired state: it is a fact recorded because it cannot be recomputed, which is what observed state is. It is an input to no decision `Reconcile` makes; the reconciler neither reads nor writes it.

Written **on the construct path only**, because that is the one place the value is proved rather than assumed. `issueAgentCertificate` "answers for an agent assigned to the calling operator at the current epoch" and refuses every other caller, so a certificate in hand means the epoch in hand is the one this operator holds the agent at. Nowhere else can say that.

Never refreshed afterwards, and this is the load-bearing half. An epoch re-read from a later `listDefinitions` would be whatever the assignment stands at — current whoever holds the agent by then — so a report carrying it could never be found stale, and the fence the route exists to provide would be inert for this caller by construction.

### Two of `garam`'s four states are reachable, and the mapping says so

| What this operator observed | What it reports |
|---|---|
| The workload reports a ready replica | `ready` |
| The workload reports no ready replica | `provisioned` |
| The workload was not read at all | nothing is sent |

`failed` is **unreachable**. Telling a replica that cannot start from one that has not started yet needs container statuses, which a StatefulSet's status does not carry. `Available=False` covers both and says neither, so a report of `failed` drawn from it would assert more than was observed.

`pending` is **unreachable, and needs nothing**. It is `garam`'s word for an agent no operator has reported on yet, and `garam` already holds it before any report. Sending it would be a report asserting that this operator has never reported. The way to leave `pending` standing is to stay silent, which is what an unobserved workload gets — the latch working in the project's favour for once.

### Every pass reports every agent, and nothing remembers what it sent

A third `manager.Runnable`, `garam.Reporter`, beside the `Poller` and the `Renewer`, on `--garam-report-interval`.

Unconditional rather than on change, because the latch inverts the usual argument. `garam` expires nothing, so a report skipped is a report that never happens and no later pass corrects it. On change makes the stored state depend on this operator's memory of what it last sent being right across restarts; unconditional makes the stored state equal to the last thing observed, with no memory to be wrong. Where a value is a latch, the safe write pattern is the unconditional one. What it costs is one request per held agent per interval, on a route that answers `204`.

Beside the poll rather than inside it, on the reasoning [ADR 0008](0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md) gave the `Renewer`: the two answer to different clocks and a call one makes should not hold the other's up. Its subject differs too — a poll reads `garam`'s definitions and this reads the cluster's `Agent`s — and reading the `Agent`s is what makes it cheap, because one list answers every agent where a loop driven by definitions would read each agent's object separately.

Not from the reconciler. `agent.md` says "The reconciler reads the `Agent` and nothing else", and an unset `--garam-address` leaves this operator making no outbound call because the whole `garam` block is behind that guard rather than because a nil check is remembered at each call site. A reconciler holding a `garam` client would break both, put a 30-second call on the hot path, and make a level-triggered idempotent `Reconcile` responsible for a side effect on a latch.

### A refusal ends that agent's report and nothing else

`failed_precondition` is reported as `ErrReportStale` and `403` as the existing `ErrAgentNotHeld`. Both are terminal for that agent and neither is retried: no route answers the epoch this operator holds an agent at, and a definition's claim answers the assignment's rather than this operator's, so nothing recovers a stale one. Both are logged and the pass goes on to the next agent, because one agent this operator no longer holds says nothing about the rest.

Neither tears anything down. That follows the answer `agent.md` already records for the same fact met at the certificate route: this operator learns it was replaced and acts on nothing, because the credential in the pod is the agent's to renew and `garam` publishes nothing that says an operator should stop. The stale refusal is a second place that fact surfaces, not a new decision about it.

## Consequences

- **`garam` can tell a CrashLoopBackOff agent from a healthy one.** A healthy agent reads `ready` and a crash-looping one reads `provisioned`, which are different values where both were `pending` before. What `garam` still cannot tell is a crash-looping agent from a slowly-starting one; the issue's goal was that `garam` learn what this operator sees and learn nothing it did not observe, and that is met.
- **`ready` is worth exactly what the `Available` condition is worth.** A ready replica means the containers are running and says nothing about whether the agent inside them works. Issue #3 records that a broken agent inside a running Pod is invisible from here and this does not close it — it carries the same boundary one repository further out, which is why the bound is written into `ProvisioningState`'s own documentation where a caller meets it.
- **An `Agent` constructed before this change is never reported on.** It carries no `status.epoch`, `claimDefinition` will not answer one again, and the construct path is not re-entered while its credential is placed. Reporting it at the assignment's current epoch is the laundering this decision refuses, so it stays silent and `garam` holds `pending` for it — which is true. What clears it is the credential being replaced, or the `Agent` being rebuilt.
- **A report is up to one interval late.** The observation happens in `Reconcile` and the report on the reporter's clock. Given a latch `garam` never expires and a console that does not yet call `listAgents` (`garam`'s issue #838), a minute of lag is not worth reshaping the operator for.
- **The traffic to `garam` grows by one request per held agent per interval.** It is bounded and it does not vary with events.
- **`AgentStatus` gains a field that only the poller writes.** Both writers of an `Agent`'s status already patch rather than update, per issue #84, and this adds a field to the patch the poller already sends rather than a second writer.
- **No new RBAC.** The reporter lists `Agent`s through the manager's cache, and `agents: get;list;watch` is already granted.
- **An operator given no `--garam-address` still makes no outbound call.** The `Reporter` is constructed inside the same guard the `Poller` and `Renewer` are, so the property is kept by the shape rather than by a check.

## Rejected alternatives

- **Hold the epoch in memory on the poller.** Free, and fatal. A definition is claimed once, so after any restart — a rollout, a node drain, leader election moving — the epoch is unobtainable for every agent claimed by a previous process, and because the state is a latch, whatever those agents were last reported as stands forever.
- **Re-read the epoch from `listDefinitions` on each pass.** Free, durable, and dishonest. `api/machine.yaml:322-330` says the field carries the epoch of whoever holds the agent by then, so sending it reports `garam`'s own answer back to `garam` as this operator's belief, and a report that is current by construction can never be refused as stale. It also does not avoid needing a home: the observation happens in a reconcile, so the value would have to be cached in memory anyway — which is the alternative above — or fetched by the reconciler to answer a condition, which `agent.md` refuses on issue #72's ruling that a condition worth a second read is worth a watch instead.
- **Keep the epoch in an annotation.** Not API surface: untyped, unvalidated, unpublished, and invisible to a reader of the CRD. `AgentStatus` already carries the other half of the same answer.
- **Put the epoch in `AgentSpec`.** It is not user intent and no user can write it. `agent.md` settled the identical question for the GRN.
- **Report `failed` when `readyReplicas` is zero.** It is the state `garam` most wants and the one this operator cannot earn. A mapping that reports three states honestly is better than one that reports four with a guess in it.
- **Report `pending` for a workload this operator did not read.** It is a false statement about this operator's own history — the report itself disproves it — and it is unnecessary, because `garam` already holds `pending` until something says otherwise.
- **Report on change rather than every pass.** It saves requests the project is not short of and buys a correctness dependency on remembering what was sent, across restarts, against a value nothing expires.
- **Add a Pod watch now, so `failed` becomes reachable.** Refused here, not forever. Issue #72's ruling is that a condition worth a second read is worth a watch instead, so a watch is the right shape if the answer is wanted — this decides that the answer is not wanted yet. Telling starting from cannot-start is a change to what the operator observes, so it would move the `Available` condition and the sentence bounding it, which is issue #103's ground and not this one's; it needs `pods` RBAC this operator does not hold, a mapping from Pod to `Agent`, and a reading of container statuses that has to distinguish `CrashLoopBackOff`, `ImagePullBackOff` and `ContainerCreating` from each other. What would settle it is a deployment that has to act on the difference — and `garam` publishes nothing an operator should do about `failed`, which is the same gap `agent.md` records for a reassignment.
- **Send the report from the reconciler.** Named as the target path in issue #117 and refused on three grounds, given in the decision above: it breaks "the reconciler reads the `Agent` and nothing else", it moves the unset-address no-op from a structural property to a remembered nil check, and it puts an outbound call with a 30-second timeout on the reconcile path.
