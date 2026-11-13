# ADR 0027: An agent's tools arrive in its own image, and this operator mounts no tool tree

> Status: accepted
> Date: 2026-09-27

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0019](0019-mount-an-agents-tool-tree-from-an-image-this-operator-names.md) mounts an agent's tool tree from an image named by `--agent-tools-image` and points the agent at it with `SHERLOCK_TOOLS_DIR`. Its ground was the agent image's own build: `sherlock@b61451e:build/agent.Dockerfile:20-24` shipped no tool tree by design, so the tree had to come from somewhere else.

That ground no longer holds:

- **`sherlock` bakes its first-party tools into the agent image.** `garamsh/sherlock#852`, merged as `35e79b5`, builds every tool into `/opt/sherlock/tools/` in the agent stage and sets `ENV SHERLOCK_TOOLS_DIR=/opt/sherlock/tools` in the image itself (`sherlock@35e79b5:build/agent.Dockerfile:70-77`), under that project's ADR 0021 (`sherlock@35e79b5:docs/architecture/adr/0021-tool-baked-into-agent.md`). The same file says no separate tools image is published (`:6-7`).
- **The tools image is gone.** The owner confirmed on 2026-09-27, recorded on issue #173, that `garam/sherlock-tools` no longer exists in ECR. Nothing is left for `--agent-tools-image` to name.
- **Issue #142 asks whether a tools image exists.** This answers it: none does, and none is expected.

One deployment still passes the flag. `garamsh/gitops@3e4d697:apps/gagent-operator.yaml` appends `--agent-tools-image` to the manager's arguments, and `garamsh/gitops#267`, open at the time of writing, removes it. That Application pins this repository's `config/default` by commit and the manager image by digest, so it runs this change only when that file moves. The edit that moves it can drop the argument too.

## Decision

**This operator names no tools image and builds no tool tree into an agent's Pod.** `--agent-tools-image` is removed. The Pod carries no `tools` volume, the agent container has no mount for one, and this operator sets no `SHERLOCK_TOOLS_DIR`. The agent image sets that variable itself, and a variable this operator wrote would override it.

**Which tools an agent has is decided by the agent image.** `--agent-image` names the build, and the build carries the tools. A cluster changes its agents' tool set by naming another build, the way it changes anything else in the agent.

**ADR 0019 is superseded in full.** Every clause it decided depends on an image this operator named: the image volume, its mount path, the variable, the pull policy, and the startup notice for an unset flag. None of them has a subject left.

**[ADR 0012](0012-declare-an-agents-tool-set-in-its-definitions-values.md) stands.** `tools-dir` stays out of the closed set of keys a definition's values carry, and a definition still names no directory. What changes is who writes the directory. ADR 0019 made this operator write it and ADR 0012 said this operator decides it. This operator now writes nothing, and the image decides. ADR 0012's pins, its closed key set and its file are unchanged. `spec.tools.pins` still selects within the tool set, which is now the image's.

## Consequences

Easier:

- An agent built from a published `sherlock` image finds its tools where it looks, and no second artifact has to exist first.
- A cluster no longer needs the `ImageVolume` feature for an agent to start. The Pod carries one image fewer to pull on every start.

Harder, or ruled out:

- **An operator passed `--agent-tools-image` refuses to start.** Go's `flag` package rejects an argument it does not define. The flag is not kept as a no-op, because a value that is silently ignored hides that it no longer does anything. The one deployment passing it moves onto this code only by an edit that can drop it. The PR carrying this decision records that order for the reviewer.
- **The tool set is no longer this operator's to choose independently of the agent image.** ADR 0019's sentence that "this operator becomes the party that says what an agent may do" is withdrawn with it: the deployer still says it, now through `--agent-image`.
- **A workload an earlier operator built with a tree loses it on the next reconcile.** The builder writes the Pod's volume list, the agent container's mounts and its environment on every pass, so nothing is left behind. An integration test covers that path.

What is not measured here: that the baked-in tools execute under this Pod's uid 65532. `sherlock@35e79b5:build/agent.Dockerfile:70-74` chowns them to that uid, and the integration layer has no kubelet to run one.

## Rejected alternatives

- **Keep `--agent-tools-image` as an optional override.** The `sherlock` Dockerfile notes that a volume mounted at `/opt/sherlock/tools` overrides entries (`:72-73`), so the mechanism would still work. It is refused because nothing exists for it to name. A flag with no artifact behind it is what `configuration.md` already recorded as an open question. An override image, once one exists, is a new ADR.
- **Accept the flag and ignore it,** so that a deployment still passing it keeps starting. It keeps a configured value that does nothing, and says so to nobody. The deployment it would protect already pins the build it runs.
- **Keep writing `SHERLOCK_TOOLS_DIR` with no volume,** pointing at the image's own path. The image already sets it. Writing it again only makes this operator the second author of a path the image owns, and a relocation there would break silently here.
