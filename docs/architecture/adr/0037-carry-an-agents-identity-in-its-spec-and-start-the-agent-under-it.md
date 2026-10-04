# ADR 0037: Carry an agent's identity in its spec, and start the agent under it

> Status: accepted
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #219. An agent's identity is the GRN `garam` minted for it and the assignment epoch this operator was proved to hold it at. Before this change both lived only in `status.agent` and `status.epoch`. [ADR 0007](0007-claim-definitions-from-a-poller.md) put the GRN there: "A claimed agent GRN is reported in `AgentStatus`, never written in `AgentSpec`." [ADR 0016](0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md) put the epoch beside it for the same reason.

Two consumers now need the identity inside the Pod:

- **`sherlock` refuses to start without an agent ID.** At `sherlock@0ced773` (its #935 and its ADR 0016), `--agent-id` is required, has no default, and is read off the flag only (`cmd/sherlock/agent.go:71,79,99`). The launcher is meant to pass the GRN, verbatim. The image's `CMD` is `agent` alone (`build/agent.Dockerfile`), and this operator passed no ID, so no agent it built on an image at or after that commit starts. The same commit adds `--assignment-epoch`: optional and opaque, with empty meaning unknown (`:72,102`). An image that predates the flag refuses it as unknown.
- **`garam`'s adapter requires the GRN and the gateway's agent ID.** At `garam@92c1eb5`, `GARAM_ADAPTER_AGENT` and `GARAM_ADAPTER_GATEWAY_AGENT` are required (`internal/cli/delivery.go:80-92`). Placing the adapter is issue #216, which builds on this change.

Rendering the identity into a Pod makes it a reconcile input. `stack-kubebuilder.md` §3 forbids reading Status as an input to the decision Reconcile makes, so the identity has to be in Spec. The PM decided the shape on issue #219.

## Decision

**`AgentSpec` carries an optional `identity` with `grn` and `assignmentEpoch`, both opaque strings.** A CEL rule on the spec refuses any update that changes `identity.grn` or removes `identity` once it is set. Setting it on an `Agent` that carries none is accepted, and so is moving the epoch. Only the writer of `identity` moves the epoch: today that is the constructor, and later the control-store render (#217). A CEL rule cannot tell writers apart, so this is stated on the field rather than enforced.

**The constructor writes `identity` when it builds an `Agent`, from the values the certificate route proved.** ADR 0016's rule still holds: the epoch is written where it is proved. It now also lands in the spec as the rendered input, and the status keeps reporting what was observed. An epoch of zero is no epoch, because `garam`'s epochs start at one.

**An `Agent` constructed before this change gets `identity` filled in once from its own status.** The step that keeps `spec.image` current ([ADR 0018](0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md)) does it, renamed from `CorrectImage` to `CorrectSpec` because it now corrects two fields. It runs on the poller's clock for every claimed agent, before the credential is looked at. Construction is never reached again for an agent whose credential is placed, so this step is the only route left. Only Agents this operator owns are filled, meaning those whose `status.agent` names the claimed GRN. The fill never runs over an `identity` already set, so no GRN is minted or moved and every name derived from the GRN stays as it is.

**The agent container is always started as `agent --agent-id <id>`.**

- The ID is `spec.identity.grn` where one is set.
- Otherwise it is the `Agent`'s `metadata.name`. That is a development identity for a hand-written `Agent` ([ADR 0031](0031-support-agents-declared-in-garam-on-one-cluster-of-the-sherlock-type.md) §4): it is unique in its namespace and is not a GRN.
- `--ego-file` follows where an ego is declared ([ADR 0035](0035-carry-an-agents-model-and-ego-in-its-spec-and-render-them-through-its-types-descriptor.md)).
- `--assignment-epoch <epoch>` follows only where the manager's `--agent-assignment-epoch` flag is on and the spec carries an epoch. The flag is off by default: the deployer sets it once the agent image the deployment names accepts the flag.

**The descriptor's argument renderer replaces ADR 0035's `egoArgs`.** It takes the agent ID, the ego file and the epoch, and returns `sherlock`'s command line. The flag names are `sherlock`'s, and `implemented()` still requires the field.

**The reconciler reads no Status field to build the Pod.** It reads `spec.identity` and `metadata.name`.

## Consequences

- An agent on a `sherlock` image at or after `0ced773` starts again, under its GRN. An agent on an older image is started with `--agent-id` too, and nothing refuses it: the flag predates `0ced773`. At `sherlock@07aa5c4:cmd/sherlock/agent.go:68` it defaulted to `default`, and `--assignment-epoch` did not exist. The epoch is the only part of the command line that depends on the image, which is why it alone has a switch.
- **Every agent's Pod rolls once.** The agent's arguments change for every `Agent`: by `--agent-id <grn>` once the identity is filled, and by `--agent-id <name>` for a hand-written one. The StatefulSet, its claim and the volume keep their names and their objects, so the memory store stays where it was. A running session is lost, which is the price [ADR 0018](0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md) already records for a corrected image.
- **A hand-written `Agent` answers on `/agents/<name>/`.** No `garam` adapter can reach it, because no GRN names it. That matches what ADR 0031 §4 already says such an `Agent` is: development only, and invisible to `garam`.
- **The GRN is stated in two places, so the two could disagree.** The spec is what the agent is started under, and the status is what this operator reports to `garam`. They agree because one writer writes both. The rule makes the spec's GRN unchangeable, and nothing writes the status's twice.
- **A person can write `identity` on an `Agent` they wrote.** The agent is then started under that GRN, and the rule holds them to it. This operator does not report on such an `Agent`, because its status names no GRN.
- ADR 0007's sentence "never written in `AgentSpec`" and ADR 0016's "it is Status and not Spec" are superseded in part. Both values are still reported in status for the reasons those ADRs give. The spec now carries the rendered copy. The rest of both decisions stands.

Ruled out:

- **Reading `status.agent` in the reconciler.** It is the shortest path, and it is the read `stack-kubebuilder.md` §3 forbids. A status the reconciler builds from would be desired state wearing an observed label.
- **An annotation.** It would be untyped, unvalidated and invisible in the CRD, the ground ADR 0016 gave for refusing one for the epoch.
- **Passing `--assignment-epoch` unconditionally.** An image without the flag refuses to start on it.
- **Starting a hand-written `Agent` with no `--agent-id`.** `sherlock` refuses to start, so every development agent would stop working.
