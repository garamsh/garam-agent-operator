# ADR 0019: Mount an agent's tool tree from an image this operator names, and point the agent at it

> Status: accepted
> Date: 2026-09-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Two agents this operator constructed run the correct image and neither starts. Measured on `admin@garam-dev` in `ns/garam-agent-operator-system` and recorded on issue #123: `agent-95450af4dabb6d5b-0` and `agent-ca7fa497ab68f187-0`, 32 and 35 restarts, both exiting 1 on `tool registry: tools directory "./tools" does not exist`. The agent container carries no `command` and no `args` and mounts a credentials volume, a state volume and the API-access projection — no tool tree of any kind.

**The tree is this operator's to supply, and that is settled by the image's own build rather than only by a division this repository drew.** `gagent@b61451e:build/agent.Dockerfile:20-24` describes the tool client as the one that reads *the operator's* tool manifests, so the published agent image ships no tree by design; issue #123 records pulling that image's 14 layers and listing every path — 1479 of them, 0 matching `tool`. There is no packaging oversight to fix one repository over.

**Failing closed is `gagent`'s decision and stands.** Issue #123 records the re-examination that moved this fix here: the required tool is the mouth, so an agent started without a tree cannot report that it has no tree; `RuntimeState` is derived from the workload, so a mute agent binding its port would read as healthy rather than truthful; and a registry failure today means no listener, which is the signal a TCP connect proves. An agent with no tree has been misconfigured by its operator, and refusing to start delivers that to the only party who can fix it.

**What the image expects was measured rather than assumed.** The published agent image's config blob declares `WorkingDir /home/nonroot`, `Entrypoint ["/gagent"]`, `Cmd ["agent"]` and `User nonroot` — received through this repository's PM on 2026-09-04. So the default `./tools` resolves to `/home/nonroot/tools` and not to `/tools`, which is what a reader assuming a distroless base with no working directory would expect. Nothing below rests on that default; it is recorded because a mount placed at `/tools` would look correct and miss it.

**[ADR 0012](0012-declare-an-agents-tool-set-in-its-definitions-values.md) is re-examined here and stands, with one clause narrowed to what it decided.** That ADR decides where an agent's tool set is *declared* — in a definition's `values`, as a closed set of keys — and it says of the directory: "`tools-dir` is not one of the keys, because it is construction … This operator decides it and writes it beside the pins." The decided half is the first sentence, and this ADR keeps it whole: the directory is this operator's and no definition names it. The four words "beside the pins" describe where a setting would be written in a file that has not been built — ADR 0012 landed with no mechanism, nothing in this repository reads a value yet, and that ADR's own §Consequences leaves "which object renders the declaration into a file in the Pod" undecided. No fact in ADR 0012 is wrong, so this is not an erratum: its table records `tools-dir` as resolving from the environment as `GAGENT_TOOLS_DIR`, and it is right about the pins.

**Its "one road" clause is not crossed either, and this is written down so it is not argued again when pins land.** ADR 0012 refuses "a per-key choice … for a set with one member". Its subject is the closed set of keys read out of a definition's `values`, and by its own sentence above `tools-dir` is not in that set. When the pins arrive as a file beside a `GAGENT_TOOLS_DIR` in the environment, that is two categories carried two ways and not two roads for one category. The reason the environment is refused for the pins does not reach the directory either: `gagent` passes `os.Environ()` unmodified to every tool it spawns, which is why a pin set in the environment is readable by the tree it exists to protect, and a path the tree is already mounted at is not a secret from the tree.

## Decision

**The tool tree reaches an agent's Pod as an OCI image this operator names, mounted read-only as an image volume.** A new flag, `--agent-tools-image`, names it, parallel to `--agent-copy-image`: both are images the workload carries that no `Agent` names, and both come from this operator's configuration. The Pod carries it as `pod.spec.volumes[].image`, so the kubelet pulls and mounts the image itself.

**The volume is mounted at `/opt/gagent/tools`, an absolute path this operator chooses.** It is outside the state volume's path, which the agent writes and this tree is no part of, and it is named explicitly rather than left to resolve against a working directory in somebody else's image.

**The agent is pointed at it with `GAGENT_TOOLS_DIR` on the agent container.** The image's own `ENTRYPOINT` and `CMD` are left intact: this operator sets no command and no arguments on that container today, and the environment is the road that keeps it that way. `GAGENT_TOOLS_DIR` resolves in `gagent`, measured on issue #92 and recorded in ADR 0012's table, and the name is that project's because this operator is writing that project's setting.

**The image volume is pulled at every start**, on the ground `agent.md` already gives for the init container's image rather than the one it gives for the agent's: the reference comes from this operator's configuration, nothing here requires its tag to name one build, and a node's cache would otherwise leave two agents running different tools under one name.

**Unset builds the workload this operator built before a tool tree could be named.** No volume, no mount, no environment variable, and the whole feature unconstructed — the shape `--garam-address` already uses for a deployment that has decided nothing. It is said once in the manager's log at startup and nothing else is done about it, because an agent that finds no tree says the rest itself by refusing to start. The flag is not required: making it so would refuse to start every deployment that has one today, over an image that exists in no registry.

**Only the agent's container gets the tree.** The init container copies a credential and has no use for it, and a mount is the smallest thing that can be given to one container rather than to the Pod.

## Consequences

Easier: an agent this operator constructs finds a tool tree where it looks, and which tools its agents get is the deployer's choice in the literal sense ADR 0012 means — the flag names the image, and the image carries the tree.

**Nothing here has been exercised against a cluster, and no image exists to exercise it with.** Issue #123 records that the tool binaries are gitignored build outputs of `make build-tools`, that neither `gagent` image copies them, and that no registry holds them. Nothing anywhere carries the built tree, so what stands between this flag and a value is an image that does not exist rather than a path to publish one — the binaries are `gagent`'s and whether that project publishes them is open with its PM, not work this repository can do. So `--agent-tools-image` names nothing until such an image exists, the e2e suite is not extended, and what has been shown here is what the operator builds and what a `restricted` namespace admits — not an agent that starts. The state issue #123 asks to verify against — an agent reporting `ready` and a turn that reaches `garam` — is not reached by this decision alone.

**What is measured here is the admission and not the execution.** On 2026-09-04, against the Kubernetes 1.36.2 API server the integration layer runs: the image volume survived the write to a StatefulSet's Pod template, and a namespace enforcing `pod-security.kubernetes.io/enforce=restricted` admitted the Pod this operator builds with that volume on it while refusing the same Pod with a `hostPath` volume added — so the standard's volume-type rule is enforced there and `image` is on the side it admits. What no layer in this repository can say is whether the agent's uid can execute the binaries the tree carries off a read-only image mount; that needs a cluster and an image, and it is recorded as an open question rather than asserted.

**The deployment's cluster has to serve image volumes.** Issue #123 records `kubernetes_feature_enabled{name="ImageVolume"} 1` on `admin@garam-dev` and `pod.spec.volumes.image` accepted there. A cluster where the feature is off would take the field and mount nothing, which presents as the crash this decision exists to remove — one more thing a deployment owes, in exchange for a mechanism that asks nothing of the image carrying the tree.

**This operator becomes the party that says what an agent may do.** The tree it mounts is the tool set, so a deployment's flag decides the capability of every agent it constructs, and a definition's pins select within that. ADR 0012 already put the second half of that sentence in `garam`'s hands, and this puts the first half where the image already was.

**The tree's contents are outside this decision.** `--agent-tools-image` names an image and nothing here reads what is in it, so an image carrying no `message_send` produces exactly the failure this decision removes, one layer further in. That is `gagent`'s check to make, and this operator has no way to make it.

ADR 0012 is extended and not superseded, as ADR 0012 extended [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md) and [ADR 0018](0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md) extended it after: every decision ADR 0012 took stands, including that the declaration is a definition's, that the key set is closed, and that the pins travel as a file. What is added is how the tree the pins select within reaches the Pod, and where the directory is written.

## Rejected alternatives

**A ConfigMap carrying the tree**, which is arithmetically excluded. Issue #123 measures the four first-party tools as compiled Go binaries totalling about 40.6 MB — `files` 14.5, `shell_exec` 14.5, `github` 8.6, `message_send` 3.0 — against a 1 MiB object ceiling. It is not a close call and no packing changes it.

**A layer in the agent image**, which moves the tool set from the operator to `gagent` and contradicts the sentence that image is built on. It would also make every tool-set change a new agent image, which is the coupling `--agent-image` exists to keep out of a definition.

**An init container copying the tree**, which is [ADR 0010](0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md)'s machinery applied where none of its reasons hold. That copy exists because a file's reader must own it and the kubelet writes root-owned files; a tool tree is executed, not owned, and needs nothing writable. The copy would also ask the source image for a shell, which is the third clause `--agent-copy-image` already carries and the one an image volume needs none of.

**Overriding the container's `command` or `args` to pass `--tools-dir`.** It would make this operator the author of another project's argument list to set one path, and it takes the image's `ENTRYPOINT` and `CMD` away from the image — a change that breaks silently the first time `gagent` changes what it runs. The environment carries the same setting and takes nothing.

**Requiring the flag, or giving it a default.** Required refuses to start every deployment running today over an image no registry serves. A default would be this operator inventing a reference nobody publishes, which is the answer `spec.image` and `--agent-storage-size` already refuse one layer out: the field asks rather than invents.

**Mounting the tree for the whole Pod.** The init container has no use for it, and a Pod-wide mount is a larger thing to give away for nothing gained.
