# ADR 0018: Keep the image of an agent this operator constructed current with its own configuration

> Status: accepted
> Date: 2026-09-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

`--agent-image` was corrected on the live Deployment from the reviewed overlay ([ADR 0017](0017-an-unreconciled-environments-values-live-in-an-overlay-here.md), applied 2026-09-03T12:13Z) and repaired no agent. The reconciler builds an agent's container from `spec.image` in `internal/controller/agent_statefulset.go`, and `internal/garam/constructor/agent.go` writes that field once, when the Agent is created, from the flag as it stood then. So a corrected flag reaches the agents constructed after it and no others.

What that costs was measured rather than argued. On `admin@garam-dev` at 2026-09-03T20:59Z, `agent-3e3e7f08d660b434` — constructed 2026-08-25 with this operator's own development image in its spec — carried 2323 restarts and was still waiting. No route in this repository reached it: a definition is claimed once, so there was no definition left to construct from, and the only other route was editing by hand a spec this operator authored, which is issue #105's defect one level down. Getting any agent onto the corrected image took two new definitions, and that workaround spends a definition per repair.

**[ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md) is what makes this this operator's to fix.** It decided that the image comes from this operator's own configuration and that no value of a definition names it, so this operator is the only writer of the field on an `Agent` it constructed. Nobody else has a reason to set it, and an operator that writes a spec and then declines to maintain it is initialising rather than reconciling.

**The objection that stood in the way does not hold.** Issue #121 framed the question as two-sided on the ground that telling an `Agent` a user wrote from one this operator constructed is a distinction the API deliberately does not draw. The API draws it: `AgentStatus.agent` carries the GRN a construction recorded and is empty on an `Agent` a user wrote, which `agent.md` already records and `internal/garam/constructor/observation.go` already reads for the same purpose. Measured on `admin@garam-dev` at 2026-09-03T20:59Z: of the four `Agent` objects in the cluster, `agent-sample` carries no GRN and the three constructed ones each carry theirs.

## Decision

**This operator keeps the `spec.image` of an `Agent` it constructed at the image its own configuration names.** A pass that finds the two apart writes the configured one; the pull request that lands this is issue #121's.

**What tells a constructed `Agent` from a written one is `status.agent`.** The correction is refused on an object whose recorded GRN is not the agent being corrected, which covers an `Agent` a user wrote — an empty field — and one standing at a constructed name for any other reason. No new marker and no new field: the distinction the API already publishes is the one used.

**The correction belongs to the constructor and runs on the poller's clock, not the reconciler's.** Two reasons, and either alone decides it. `stack-kubebuilder.md` §3 refuses reading Status as an input to the decision Reconcile makes, and `status.agent` is exactly that read. And `agent.md`'s "the reconciler reads the `Agent` and nothing else" is left standing: Reconcile goes on treating a written and a constructed `Agent` as the same kind of thing, and the asymmetry stays in the construction path, which is the only part of this operator that already knows the difference. The poller is the constructor's clock already and visits every definition this operator holds a claim on, which is the set of agents it built.

**Only the image.** `spec.storageSize` is the other field this operator supplies, and it is not taken with it: a StatefulSet's claim template is immutable after creation, so writing that field would move an object into the state `agent.md` already records as unactionable rather than repair it. What would settle it is a route that resizes a claim.

**A pass that changes nothing writes nothing.** The reconciler builds the workload from `spec.image`, so an unconditional write on every poll would roll every agent's Pod on every interval.

**The correction runs before the credential is looked at.** Construction stops at an agent whose credential is placed, because a certificate asked for with nowhere to put it is key material created and dropped — ADR 0009's rule, untouched. Every agent this decision repairs is on that side of the branch, so a correction gated on construction would repair none of them.

**The grant widens by `patch` on `agents`, in the operator's own namespace.** ADR 0009 recorded `create` and not `update` as the smallest shape that placed a credential; maintaining a field is not placing an object, and this is the smallest verb that writes one field.

## Consequences

Easier: a wrong `--agent-image` stops being permanent. The flag is corrected, and within one poll interval every agent this operator constructed carries the corrected value with no human action and no definition spent.

**Correcting the image rolls the agent's Pod.** The reconciler updates the StatefulSet and the StatefulSet replaces its replica, so an agent holding a session loses it without being asked. That is reported here rather than worked around: nothing drains the agent first, nothing bumps an epoch, and this operator has no route to ask the agent whether it is busy. It is recorded in `agent.md` §Open questions, and it is the price of a repair whose alternative today is an agent that never runs at all.

**A hand-edited `spec.image` on a constructed `Agent` is undone within a poll interval.** That is the intended reading of "this operator is the only writer of the field", and it is a behaviour change for anyone who was using the edit as the workaround.

**Nothing converges where `--garam-address` is unset.** The whole `garam` side sits behind that guard, and `--agent-image` is required only where it is set, so an operator that reads no definitions also has no configured image to converge to. Nothing is given up that a deployment could have had.

ADR 0009 is extended and not superseded, as [ADR 0012](0012-declare-an-agents-tool-set-in-its-definitions-values.md) extended it: every decision it took stands, including that the image comes from this operator's configuration, that no definition names it, and that a credential is placed once. What is added is that the field it decided is maintained rather than only written.

## Rejected alternatives

**Reconciling the field in `Reconcile`,** which is where issue #121's decision comment put it. It reads `status.agent` to choose what to write to `spec`, and `stack-kubebuilder.md` §3 refuses that read in as many words; it would also make the reconciler read this operator's configuration, which is the coupling `agent.md`'s sentence exists to prevent. The placement above needs neither.

**A `Runnable` of its own, sweeping the namespace for constructed Agents.** It would converge an agent whose definition `garam` no longer answers for, which the chosen shape does not. `simplicity.md` §Rules refuses the division: the poller is already the constructor's clock, and the set it visits is the set of agents this operator built. A definition that stops being answered for is the reassignment case `agent.md` already carries as an open question, and it is not repaired by writing an image.

**Telling a constructed `Agent` by its name alone.** The name is the digest of the whole GRN, so a match is strong evidence, but it is evidence this operator computes rather than a fact the object carries, and `status.agent` is both published in the API and already read for this purpose.

**An ownership marker on the field or the object,** offered in issue #121's decision comment as the safety if one were wanted. It adds a second thing to keep in sync with a distinction the API already publishes, and the object it would mark is one this operator created.

**Draining the agent, or bumping an epoch, before the roll.** Both assert something about the agent's work that this operator cannot observe: it reads a replica count and no readiness probe, and `garam` publishes no route that asks an agent whether it may be replaced. Reported as an open question instead of built on a guess.
