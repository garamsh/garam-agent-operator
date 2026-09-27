# ADR 0006: Carry credential access on a group, not on a user

> Status: accepted
> Date: 2026-08-17

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The operator places an agent's client certificate and key in a Secret and mounts it into the workload as files, because the design forbids key material travelling through environment variables. It first mounted them at mode `0400` — readable by the owner alone — which is the mode a credential ordinarily wants.

The files a Secret volume produces are owned by root. An agent image that drops root, which a well-built one does, therefore cannot read its own credential. Nothing caught this for four merged changes: every layer up to and including envtest asserted the StatefulSet's specification, and the specification was correct. The e2e layer read the file from inside a running container and found `Permission denied` for a `runAsUser: 1000` Pod on the first run.

Four mechanisms were considered.

**Matching the container's user** does not help on its own. The file is owned by root whatever user the container runs as, so an owner-only mode stays unreadable.

**Widening to world-readable** works and gives up more than the problem requires: it exposes the file to a process outside the Pod's group as well as inside it.

**A field on `AgentSpec` naming the uid the image runs as**, so the operator could match it, was the candidate this decision expected to need. It turned out to need nothing: `fsGroup` is a group the kubelet *grants* to every process in the Pod, not one that has to correspond to anything in the image. Measured directly — group 65532 against an image running as uid 101, `id` in the container reporting `groups=101,65532`, and the credential read. **The premise the field rested on is false**, so the field would have bought nothing and cost an API version.

**A supplementary group with a group-readable mode** needs no knowledge of the image at all, which is the property none of the others has. The operator cannot know which user an arbitrary agent image chose, and with a group it does not have to.

One further fact decided the mode rather than the mechanism. `0400` cannot coexist with `fsGroup`: the kubelet ORs group-read into every file it writes into the volume, so an owner-only request is delivered as `440` regardless. Changing the constant is what makes the declaration match what the cluster does; it is not what grants the access.

## Decision

The Pod carries `securityContext.fsGroup` 65532, and the credentials volume is mounted at mode `0440`. Access to the credential is carried by the group, not by the user.

## Consequences

Easier: an agent image may run as any user it likes and still read its credential, and the operator never has to be told which. The same group covers every volume in the Pod.

Harder, and this is what the decision gives up: **any container in the Pod that mounts the credentials volume can read it**, where owner-only would additionally have required it to run as root. Today the workload has one container and one mount, so the set of readers is unchanged — the agent's own process. What changed is where the guarantee comes from: which containers the operator mounts the volume into, rather than which user they run as. A sidecar added later reads the credential without needing root, and whoever adds one owns that.

Neither mode protected the file from something reading the kubelet's directory on the node. That was never the boundary.

Not decided here: the rest of the Pod's security context. `runAsNonRoot`, `seccompProfile`, `allowPrivilegeEscalation` and capability drops each constrain what a user's image may be, which is a statement about the API's contract rather than a fix for this defect.

Ruled out: matching the container's user, widening the file to world-readable, and a field on `AgentSpec` for the image's uid — the last on a measurement rather than a preference.

## Errata

### 2026-08-23 — the group is not granted to every process in the Pod

Context rejects a uid field on `AgentSpec` with this: "`fsGroup` is a group the kubelet *grants* to every process in the Pod, not one that has to correspond to anything in the image." The second clause is what the rejection rests on and it holds. The first is wrong.

The kubelet puts the group in the supplementary set of each container's initial process. Descendants inherit it, and a descendant that calls `setgroups(2)` loses it. `gagent` starts such a descendant: running as root it gives a shell child `syscall.Credential{Uid, Gid}` carrying no groups (`gagent@757dd26:internal/workspace/shell/process_linux.go:174-182`), which Go executes as `setgroups(0, NULL)` (`syscall/exec_linux.go:491-497`, go1.26.0).

Measured on kubelet v1.36.3, in a Pod carrying `fsGroup` 65532 with the credentials volume mounted as this decision specifies. The container's initial process reports `groups=[0 10 65532]` and reads the credential; a child spawned that way reports `groups=[]` and is refused it.

The decision stands. The credential is read by the container's initial process, which is the agent, and reaching that process is what the group was chosen for. What does not follow from it is that a file this mechanism delivers group-readable is readable by everything the Pod goes on to run.

Falsified on issue #57.
