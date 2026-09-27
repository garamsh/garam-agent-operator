# ADR 0017: An environment's values live where that environment is reconciled, and in an overlay here where nothing reconciles it

> Status: accepted
> Date: 2026-09-03

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0013](0013-the-base-carries-what-every-deployment-shares.md) made two decisions. The first is a test that assigns each flag an owner: a flag whose value is the same wherever this operator runs is the base's, and a flag that names something one cluster holds is the deploying overlay's. The second says where that overlay lives — "the repository that reconciles that environment, which for `admin@garam-dev` is `garamsh/gitops`" — and refuses `config/overlays/<environment>/` here.

The test has held; nothing below reopens it. The assignment has produced no artifact in the three days since it was made, and the three flags it names — `--garam-address`, `--agent-image`, `--agent-storage-size` — have had an owner and no home for all of them.

Measured 2026-09-03, read-only against `admin@garam-dev` and the AWS account, and against `garamsh/gitops` at `72fff6e0`:

| Read | Result |
|---|---|
| Paths in `garamsh/gitops@72fff6e0` | 108 tracked files and directories |
| Of those, matching `gagent` | 0 |
| Of those, matching `csi` | 10, under `apps/`, `charts/` and `docs/`, for the `proxmox-csi-plugin` that repository does reconcile |
| Argo CD `Application` objects in the cluster | 10: `root`, `applicationset`, `amazon-eks-pod-identity-webhook`, `cert-manager`, `cloudnative-pg`, `external-secrets`, `garam`, `gateway`, `openbao`, `proxmox-csi-plugin` — none names this operator |
| The manager Deployment's labels and annotations | `app.kubernetes.io/managed-by: kustomize`, and a `kubectl.kubernetes.io/last-applied-configuration` |
| The field managers that had written its spec | `kubectl-client-side-apply`, `kubectl-set`, `kubectl-patch`, `kubectl-rollout`, `kube-controller-manager` |

The `csi` row is the control: the search finds what `garamsh/gitops` reconciles, and this operator is not among it. The Deployment is applied from this repository's `config/` by `kubectl apply`, which is what the label and the last-applied annotation record, and no GitOps controller has written it.

**ADR 0013's facts were true when written and are still true.** `gitops@8971dd5` held no path named `gagent`, and `gitops@72fff6e0` still holds none; `garamsh/gitops` did reconcile this cluster and still does — nine of the ten `Application` objects above are declared under its `apps/`. What fails is the step from those facts to the assignment. ADR 0013 read "the repository that reconciles this cluster" as naming the repository that would come to reconcile this operator, and the two are not the same claim. A repository can reconcile an environment and not reconcile every workload in it, which is what this one does.

The cost of the gap is measurable too. `--agent-image` on the live Deployment names `garam/gagent-operator-dev:f02d007c937e` — the operator's own image, and an older build than the operator running it — so the constructed agent's Pod runs the manager binary with no arguments and its fail-closed guard refuses to start: `agent-3e3e7f08d660b434-0` had 2216 restarts when read. The image the flag should name exists and is pullable: `garam/gagent-agent:0.1.0`, digest `sha256:8e8508a086a9707913c417a922e5c1da0fdb0c3267f7f3903d1a857668dce1a5`, in an `IMMUTABLE` repository with scan-on-push, and `garam-dev-ecr-pull` names that repository. `cmd/main.go` requires `--agent-image` wherever `--garam-address` is set, so the two are not independently deferrable.

That changes what the question is. ADR 0013 weighed an overlay here against an overlay in the repository reconciling the environment, and refused the first. The comparison now available is an overlay here against no home at all, with the only remaining way to set the value being the `kubectl set` that issue #105 filed as the defect and `configuration.md` records. That is a different comparison, and it is the one below.

## Decision

**An environment's values live in the repository that reconciles that environment. Where nothing reconciles an environment, they live here, in `config/overlays/<environment>/`, and they leave when something takes it.**

**ADR 0013's first decision is carried forward unchanged**: a flag whose value is the same wherever this operator runs is the base's, and a flag that names something one cluster holds is the deploying overlay's. It is restated here rather than left in a superseded record, because Rule 2 freezes an ADR as a unit and a reader should not have to open a superseded ADR to find a live rule.

For `admin@garam-dev` the overlay is `config/overlays/garam-dev/`. It sets all three of the flags the test assigns to an overlay, and names the operator image the cluster already runs, so that applying it changes those three arguments and nothing else.

**A reference to an image this project does not build takes the form `<repository>:<tag>@sha256:<digest>`, and where the registry holds no index the platform manifest's digest is taken and the architecture it pins is written down beside it.**

### What ADR 0013 refused, weighed against no home at all

ADR 0013 gives two reasons and one characterization. Each is answered on its own terms.

- **"Every environment that ever deploys this operator would add a directory to the operator's source, which is the coupling the base-and-overlay split exists to prevent."** The reason is sound and is not withdrawn: it is why the first clause of the decision above still sends an environment's values to the repository reconciling it, and why an environment that has such a repository adds nothing here. What the reason does not settle is the environment that has no such repository, because for that one the directory is not the alternative to a home elsewhere — it is the alternative to nowhere. The coupling this admits is therefore bounded by a test rather than open: one directory per environment nothing else will hold, which today is one, and it is removed when something takes it.
- **"A values mechanism of this project's own"** was ruled out because `garamsh/gitops` ADR 0003 already holds one and "a second here would be an abstraction built for a caller that has one." That reason stands and this decision does not touch it: no mechanism is built. `config/overlays/garam-dev/` is a kustomize overlay over `config/default`, using the patch surface ADR 0013 itself specified — an appended argument at container index 0 — and the `images` transformer `make deploy` already uses. Nothing here is a mechanism the operator would not have had anyway.
- **"An overlay parked here would be a second unreconciled artifact rather than a home."** This was written when `garamsh/gitops` was expected to carry the first one. There is no first, so this overlay cannot be second, and the defect that sentence names — two artifacts for one value, with a reader unable to tell which is the home — cannot arise from the only artifact there is. What remains of the sentence is "unreconciled", and that is not a property of the overlay: `config/manager/manager.yaml` is applied by the same `kubectl apply` and nobody calls it not a home for the five flags it carries. What makes it a home is that the value is readable in a repository and its change was a reviewed diff. An overlay in the same tree, applied the same way, has both.

### Why the reference form is settled here too

`delivery.md` fixes the reference form for the image this project builds, and ADR 0014 requires that reference to carry the image index's digest so the architecture is not pinned silently. `stack-container.md` decides the same for a base image a `Dockerfile` names. Neither reaches `--agent-image`: this project does not build that image, and no `Dockerfile` here names it. The case is unnamed, and it is settled the way the nearest rule settles its own — same form, same reason — with one difference the registry forces.

`gagent` publishes that image and binds `garam/gagent-agent` to the same shape from its own side — immutable, a tag naming one commit, a consumer referencing the digest with `imagePullPolicy: Always` (`gagent@b61451e:docs/architecture/adr/0020-immutable-image-repositories.md`) — so the form agrees at both ends rather than being imposed from this one. `garam/gagent-agent:0.1.0` is `application/vnd.oci.image.manifest.v1+json`: a single-platform manifest whose config declares `linux/amd64`, with no index above it. There is no index digest to take, so the choice is not between an index and a platform manifest but between this digest and none. The digest is taken, and the pin is recorded here, in `configuration.md`, and in the overlay beside the argument. The defect ADR 0014 names is a pin nothing records; a recorded pin is not that defect. Two things bound it: every node of `admin@garam-dev` is `linux/amd64`, read on the same day, and the pin lives in one environment's overlay rather than in the base, so it makes no claim about any environment but the one it names. If `gagent` publishes an index for this image, the reference takes the index digest and this clause stops applying.

### Why this supersedes ADR 0013 rather than appending an erratum to it

Rule 2 offers three ways to touch a merged ADR — the status field, a typo or a broken link, and an erratum "when the decision stands but a fact supporting it was wrong" — and one way to change it, which is a new ADR that supersedes. There is no amendment among them.

An erratum is the wrong instrument twice over. It requires a supporting fact found wrong, and every fact ADR 0013 states is still true, three days later and at a later commit of the repository it names; and it requires the decision to stand, which is exactly what is not the case. An erratum here would record a correction to a record that needs none, under a decision that no longer holds — the reader would find the assignment intact and a note that changed nothing about it.

So it is a supersession, of ADR 0013 as a whole, because the ADR is the unit Rule 2 freezes. That is why the first decision is restated above rather than cited: superseding one half and leaving the other live in a superseded file would make the current rule findable only in a record marked as no longer current.

ADR 0013 names its own supersession triggers — a value the base cannot express through an appended argument, or a deployment by something that is not a Kustomize consumer — and neither has fired. That list was not exhaustive and does not bind; the trigger here is the third case, which it did not anticipate: the appended argument works and the consumer is a Kustomize consumer, and the repository assigned to do the appending is not one.

## Consequences

- **`--garam-address`, `--agent-image` and `--agent-storage-size` have an artifact.** A reader answers "where does this value come from" from this repository, for all three, which is what issue #105 asked and ADR 0013 could not deliver. `--agent-image` was the flag that forced the question, but moving it alone would leave the other two exactly where it was found, and `cmd/main.go` refuses to start with an address and no image, so the two travel together by the manager's own rule.
- **This repository now contains one environment's values, and can contain more.** That is the cost. It is bounded by the test — an environment reconciled somewhere puts nothing here — and it is reversible: the overlay is deleted when a reconciler takes the values, and the base does not change when it is.
- **Applying it is still a human act, and this does not make one.** The overlay is an artifact; nothing in this repository applies it, and the field managers on the live Deployment stay what they are until something does. `configuration.md` keeps that as an open question.
- **The overlay states an operator image, and so it can go stale.** It names the build the cluster runs so that applying it moves only the three flags. A published operator image that is not written here leaves the overlay describing an older deployment than the one running, with nothing in this repository noticing — the same class of gap `delivery.md` records for the manual publish path.
- **The agent reference pins `linux/amd64` for this environment.** A node of another architecture joining `admin@garam-dev` would fail to run an agent, and the failure would be a Pod that cannot pull rather than a silent mismatch.
- **`config/overlays/` exists, and the scaffold expects it to.** `stack-kubebuilder.md` §1 already describes `config/` as "kustomize bases and overlays", so no path the kubebuilder CLI owns is displaced and nothing about the generated set changes.

## Rejected alternatives

- **Leaving ADR 0013's assignment as it stands and waiting for `garamsh/gitops`.** It is the option with no cost to this repository and it is the one measured: three days, no artifact, 2216 restarts, and no path in that repository at either commit anyone has looked at. Waiting is a decision to keep the value only in a cluster, where the sole record of it is a field manager's name.
- **A different reconciling repository — `garamsh/infra`, or a new one.** It is the same shape as the decision being superseded: a home in a repository this project may not write, which delivers a statement here and an artifact nowhere. ADR 0013's own consequence excludes the fix that would make it real, that "a fait accompli in someone else's repository was excluded by the issue." Naming a second such repository would be the first mistake repeated with a different name on it.
- **Setting the value with `kubectl set`.** One command, and it ends the crash loop. It also leaves three of this operator's eight flags existing in no repository, written by the mechanism issue #105 filed as the defect, which makes the cluster better and the record worse.
- **Moving `--agent-image` alone and leaving the other two where they are.** It would answer the crash loop and not the issue: the same argument applies unchanged to `--garam-address` and `--agent-storage-size`, and refusing them a home for now would mean deciding this question twice.
- **An overlay here named for something other than the environment**, so the repository does not appear coupled to one cluster. The values name a service address in one cluster, an image in one account, and one cluster's storage. A neutral name would hide whose they are without changing whose they are, which costs the reader the only thing the directory name tells them.
- **Referencing the agent image by tag alone.** `garam/gagent-agent` is `IMMUTABLE` today, so the tag identifies content today. ADR 0014 refuses this on the ground that a consumer can read a repository's setting but not its history, and that ground does not weaken for an image published by another project — it strengthens, since this project does not own the setting either.
- **Refusing the reference until an index exists.** It would apply ADR 0014's rule past the images it governs, to make a point about an architecture this cluster is entirely built from, at the price of the agent never starting.
