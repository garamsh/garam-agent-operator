# ADR 0010: Copy an agent's credential into a memory volume the Pod's own user owns

> Status: accepted
> Date: 2026-08-26

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The process that reads the private key this operator places is `garam`'s adapter, a sidecar in the agent's pod, and it reads it under a rule with two halves (`garam@1ff8346:internal/keyfile/keyfile.go:30-41`): a key file is refused when any group or other permission bit is set, and refused separately when its owner is not the reading process's own uid. The comment between the halves says why the first is not enough — "Owner-only bits protect nothing from a process that is not the owner".

`garam` publishes the rule as a deployment contract rather than as an internal detail, and the paragraph closes both ways out (`garam@1ff8346:docs/architecture/deployment-contract.md` §What the binary enforces about key material): "**It is enforced, not requested** … **Plain Kubernetes Secret projection satisfies neither half** — a projected file is root-owned, and `fsGroup` changes the group owner rather than the user owner. **Relaxing the check to fit plain projection is not an option**; the arrival shape the chart renders is what the rule requires."

**This is a new decision and not an erratum on [ADR 0006](0006-credential-group.md).** ADR 0006 decided how the per-agent credential reaches a workload, and for the reader it was written against it is still right: the group carries the access, and nothing in it is false. What changed is that a second reader arrived with a rule the group cannot satisfy — a new case, not a falsified fact. `docs/architecture/README.md` Rule 2 reserves an erratum for a decision that stands on a fact that was wrong, which this is neither of, so ADR 0006's body is untouched and no note is appended to it.

**ADR 0006's rejection of a uid field on `AgentSpec` is what this decision had to reopen.** It refused the field on a measurement — "`fsGroup` is a group the kubelet *grants* … not one that has to correspond to anything in the image. The premise the field rested on is false" — and that reasoning covers the projection, which the group still delivers. It does not cover a file whose owner must be the reader.

Four things were measured before anything was built, because this repository has twice been wrong by reasoning from a document instead of a cluster. All of them on `garam-dev`, kubelet v1.36.3, on 2026-08-26, with the rule transcribed from the source above rather than the mode read by eye.

- **The shape works, in a namespace enforcing PodSecurity `restricted` at `enforce-version: latest`.** An init container running as uid 65532 wrote `mode=600 owner=65532` into an `emptyDir` of `medium: Memory`, and the container that ran after it, as the same uid, was accepted by both halves.
- **Two controls isolate the two halves.** The same reader applied the same rule to the projection the kubelet had written into the same Pod — `mode=440 owner=0` — and refused it as readable beyond its owner. In a second Pod differing only in the init container carrying `runAsUser: 1000`, the copy arrived `mode=600 owner=1000` and was refused as owned by another user. So the mode alone does not carry it, and the shared uid is load bearing rather than incidental.
- **The workload this decision builds does the same thing.** The StatefulSet the changed code produces was applied whole, with a real Secret, a provisioned claim, and `busybox` as the copy image: the credential was accepted, a file written beside it under a default umask was refused as readable beyond its owner, and the projection was not present in the agent container at all.
- **The uid is not a free choice, and it did not have to be forced.** `gagent`'s agent image is `gcr.io/distroless/static-debian12:nonroot` with `USER nonroot` (`gagent@e9c12a5:build/agent.Dockerfile`), and that image's configured user is uid 65532; this operator's own image runs as 65532 (`Dockerfile`); and the substitute image the e2e suite runs, `nginxinc/nginx-unprivileged:1.29-alpine`, whose own `USER` is 101, starts and serves as 65532. The number this decision names is the one two of the three images already carry.

One measurement decided where the copy tool comes from. **The agent's own image cannot make the copy**: `gcr.io/distroless/static-debian12` carries no shell and no `install`, so the shape that would need no configuration at all — an init container built from the image whose uid it has to match — is not available for the image this operator exists to run.

## Decision

**The credential the agent reads is a copy, made at pod start into a volume the kubelet does not write.** An init container copies each projected file into an `emptyDir` of `medium: Memory` with `install -m 0600`, and the agent container mounts that volume and not the projection. Memory-backed, so the copy never reaches the node's disk; bounded at 1Mi, because a memory volume naming no limit is bounded only by the node.

**The projection stays `0440` and is mounted only into the init container.** ADR 0006 is what makes the projection readable to a container that is not root, and that is exactly what the init container needs to read it. The two decisions compose: the group delivers the file into the Pod, and the copy is what an owner-only reader is given.

**The Pod names one user and every container runs as it.** `securityContext.runAsUser` is 65532 on the Pod, no container overrides it, and `fsGroup` stays 65532. Nothing tells this operator what uid the agent's image runs as, and **ADR 0006's rejection of a field on `AgentSpec` stands** — now for a second reason. An owner is one value for a whole Pod: the file has one, and every process that reads it must have the same one. A uid taken from any single image cannot be that value once a second image in the Pod reads the same file, and `garam`'s adapter is a second image that does. So the operator names the user rather than being told it, and the constraint moves onto the images, where `image`'s own doc comment states it.

**`runAsGroup` is not set.** The rule is about the owner; a group forced on the containers would only take away the primary group their images chose, and the measurement above shows an image relying on group 0 for its own writable paths.

**The image the init container runs is a flag on the manager, `--agent-copy-image`, with no default, required always.** It is the shape [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md) took for `--agent-image` and `--agent-storage-size` and the audience is the same one: the deployer, who is choosing a copy tool for a cluster this operator cannot see. It is required unconditionally rather than only where `--garam-address` is set, because every agent's Pod carries the init container — an `Agent` a user wrote as much as one this operator constructs. An operator that built workloads without it would reconcile successfully and leave every adapter refusing its credential, the failure shape this repository keeps meeting; refusing to start says it once, at the only moment a deployment can act on it.

**An `Agent` a user wrote is built the same way, and nobody supplies a uid for it.** The user names an image and this operator names the user it runs as, exactly as for a constructed one. There is no field to leave unset and no flag to get wrong, and what the image owes is a published part of the API rather than a value a person has to know.

**The copy is not mounted read-only in the agent container.** The rule it satisfies has the reader owning the file, and `garam`'s contract expects a refresh of such a copy to happen inside the Pod that reads it — "Whatever refreshes that copy must satisfy the arrival rule for the key on every refresh, not only the first". A read-only mount forecloses a mechanism `garam` has already written down. The projection's mount is read-only, because that one genuinely is.

## Consequences

Easier: an agent's adapter can start. Today it would refuse the credential this operator places, and the failure would be a successful reconcile beside a dead workload.

Harder, and this is what the decision gives up: **every image in an agent's Pod must run as uid 65532.** ADR 0006 could say an agent image may run as any user it likes; this cannot, because the file has one owner. The obligation is measured for the two images that exist and is stated on `AgentSpec.image`, and what would change it is an image that cannot take the uid. Where that arrives the uid becomes configuration, and it travels with the image that raised it — a field beside `image` for an `Agent` a user wrote, filled from a flag for one this operator constructs, which is how `image` already reaches both.

**The copy is made once, at pod start.** A credential replaced in the Secret afterwards reaches the projection and not the file the process reads, so replacing one is a restart. This operator places a first credential and replaces none, and `garam`'s own chart records the same limitation for the same shape, so nothing here is worse off than the working precedent — but an agent that renews its own certificate has nowhere durable to put it, and what would settle that is a measurement of what the adapter does with the file when it renews.

**A deployment that sets no `--agent-copy-image` no longer starts**, and that is a change in behaviour rather than an addition. `config/manager/manager.yaml` names one so that the project's own kustomize base and the e2e suite deploy a manager that runs; the manager itself invents nothing.

**Every agent's Pod now pulls a second image**, and a node that cannot pull it leaves the Pod in `Init`. The copy tool is a third-party image this project does not build.

**The workload is still not admissible where PodSecurity `restricted` is enforced**, and this change does not close that. Measured on the same cluster: before it, the Pod violated `allowPrivilegeEscalation`, `capabilities`, `runAsNonRoot` and `seccompProfile`; after it, the same four, because naming a uid is not the same as asserting `runAsNonRoot`. ADR 0006 left the rest of the Pod's security context undecided on the ground that each field constrains what a user's image may be, and this decision moves none of them.

Ruled out: **narrowing the projected mode**, which ADR 0006 measured cannot go below `0440` while the Pod carries a group and which would fail the ownership half one line later even if it could. **Relaxing the reader's rule**, which `garam`'s contract refuses in the sentence quoted above and which `agent.md` had recorded as one of two standing remedies. **A uid on `AgentSpec` and a uid flag on the manager**, which both buy the operator a number it does not need and cannot use for more than one image in the Pod. **The agent's own image as the copy image**, which needs no uid at all and is unavailable: the image this operator exists to run carries no shell.

Not decided here: **what the adapter container is and who adds it.** This decision makes the Pod's uid one value so that a sidecar reading the same file satisfies the rule by construction, but nothing here builds one, and `agent.md` still carries that as an open question.
