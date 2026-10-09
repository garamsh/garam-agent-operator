# Delivery

What this project publishes, where its output lands in a cluster, and who owns each of those places.

## Current decisions

- This project builds and publishes two images, each to two ECR repositories of its own in AWS account `486152169996`, region `ap-northeast-2`: one under `garam/` for releases, and one under `garam-dev/` for development builds (ADR 0066). It publishes no other image and reaches no other registry.

| Image | Entry point | Dockerfile | Release repository | Development repository |
|---|---|---|---|---|
| Manager | `cmd/main.go` | `Dockerfile` | `garam/garam-agent-operator` | `garam-dev/garam-agent-operator` |
| Control service | `cmd/control/main.go` | `build/control.Dockerfile` | `garam/garam-agent-operator-control` | `garam-dev/garam-agent-operator-control` |

- **The release repositories exist; the development repositories do not yet.** The control service's release repository, `garam/garam-agent-operator-control`, was created immutable by `garamsh/infra` on 2026-10-05. Creating the two under `garam-dev/` is `garamsh/infra`'s, and #323 tracks it.
- **`garam/` holds releases only.** A development image is never pushed there, including before its `garam-dev/` repository exists. The repository says which image and which role; the tag says which commit. Every rule below applies to each image and each repository alike.
- **The images in `garam/` today are development images, pushed before ADR 0066.** #323 moves them: each is copied to `garam-dev/` by digest, each referrer moves to the new path at the same digest, and only then is it removed from `garam/`. Copying and removing them is the PM's, after infra creates the repositories.
- That account is not this project's to set, so what follows states what this project requires of the repository. Nothing here describes a setting the account holds: a description of another account goes stale with nothing in this repository able to notice.
- **Each repository is created immutable and is never made mutable.** A tag identifies a build only where a second push of that name is refused. The setting is retroactive in effect — it is a property of the repository, not of a tag, and no tag records which policy it was pushed under — so a repository that is ever mutable leaves every tag it holds a label rather than an identifier, including tags pushed while it was immutable.
- **A release image is tagged `X.Y.Z`; a development image is tagged with the abbreviated commit hash of the commit built** (ADR 0051).

| Image | Tags | Pushed by |
|---|---|---|
| Release | `X.Y.Z`, from the git tag `vX.Y.Z` with the `v` stripped | `.github/workflows/release.yml` |
| Development | `<12-hex abbreviated commit hash>` only, under `garam-dev/` (ADR 0067) | a person, by the procedure below |

  - **No moving tags.** There is no `latest` and no moving tag.
  - **Development tags.** The commit tag is the identifier, and the `garam-dev/` repository marks the role, so there is no `dev-` tag (ADR 0067).
  - **A release's commit.** The git tag `vX.Y.Z` names it, and the image's revision label carries its full hash.
- **Every push is checked against the two namespaces first** (ADR 0067, #325). `make verify-image-ref IMAGE_REF=<reference>` runs `hack/imageref`, which refuses a reference under `garam/` unless it is `X.Y.Z` in `release.yml`'s run for the tag `vX.Y.Z`, as `GITHUB_WORKFLOW_REF` names it, and refuses a `dev-` tag or no tag under `garam-dev/`. `docker-push` and `docker-buildx` run it before anything else, and `release.yml` runs it before each push. It stops mistakes, not a determined pusher: a shell can export `GITHUB_WORKFLOW_REF`, and a bare `docker push` never runs it. Which principals may write to `garam/` is `garamsh/infra`'s.
- **Nothing in this repository checks that a development tag tells the truth.** No `Makefile` target compares the build context against the commit, so a development tag is a claim its pusher typed, and an image built from a modified tree carries a clean commit's name with nothing marking it. Immutability is what bounds that: it holds for the first push of a name and refuses every later one. A release is built by the workflow from the tree it checked out at the tag, so the same gap does not arise there.
- **Both images build from the repository root through one `.dockerignore`**, which ignores everything and re-includes the Go source, the module files and every file Go source embeds. There is no per-Dockerfile ignore file. A file a package embeds that the ignore file leaves out stops `go build` inside the image build only, which `make ci` and the e2e suites, building outside Docker, never reach (#301). So `make ci` runs `make verify-build-context`, which checks every package's `//go:embed` files against the ignore file with `moby/patternmatcher`, the matcher BuildKit applies (`hack/buildcontext`), and fails naming each one left out.
- **The image carries the commit it was built from as the label `org.opencontainers.image.revision`**, beside `org.opencontainers.image.source` and `org.opencontainers.image.title`. `Dockerfile` and `build/control.Dockerfile` write the three from `ARG REVISION`. `make build-images` passes `REVISION`, which defaults to `git rev-parse HEAD`, to both, and so do the scaffold's `make docker-build` and `make docker-buildx` and `make docker-build-control`.
- **The label is a claim the builder typed, as the tag is.** `--build-arg` accepts any string, and a modified tree is labelled with the clean commit it was checked out from. What the label adds is who can read the claim: a consumer holding a deployed reference reads it off the image in the registry, with no person asked. It is not a check against the tree.
- **A deployment references the image by digest and carries the tag beside it**, as `<repository>:<tag>@sha256:<digest>`. Both halves are required and each answers a question the other cannot:

| Half | Answers | Why it cannot be dropped |
|---|---|---|
| Digest | Which bytes | It is the only half a runtime resolves, and it rests on nothing a consumer would have to trust. A tag's stability rests on a repository setting, and a consumer can read that setting today but not the repository's history. |
| Tag | Which source tree | A reader of the cluster gets the commit without a registry round trip, and `git show` takes it: the hash for a development image, `v<tag>` for a release. Left out, every deployed reference is opaque to a reader with no access to the account — which is this document's own subject, moved from the repository into the cluster. |

- **Every published image is one `linux/amd64` manifest, and the digest a deployment carries is that manifest's** (ADR 0051, superseding ADR 0014's index digest). `make build-images` builds both images with `--platform=linux/amd64` and with buildx's default attestations off, so neither is wrapped in an index, and the platform is stated here rather than pinned silently. The development images pushed by hand under `7c216469476d` on 2026-10-06 are the last of the old shape, buildx indexes.

- **A release is published by `.github/workflows/release.yml`**, on the push of a `v*` tag and nothing else, modelled on `garamsh/sherlock@b3c05c2:.github/workflows/release.yml`. In order, it:
  - refuses a tag that is not `vX.Y.Z`, or whose commit is not on `main`, before any credential;
  - builds both images through `make build-images`, which pushes nothing;
  - then assumes the push role;
  - pushes both images as `X.Y.Z`;
  - reads each back from the registry with `aws ecr batch-get-image`, refusing a missing image or an index;
  - prints `repository:X.Y.Z@sha256:<digest>` for each to the step summary, which is where a deployment's reference comes from.

  It has no concurrency group, so a queued release is never cancelled. A run whose second push fails leaves the version half-published, which only the next version repairs.
- **The release workflow pushes as `arn:aws:iam::486152169996:role/garam-agent-operator-github-actions`.** Its name, permissions and trust are `garamsh/infra`'s: `#292` requested it, `#293` names it, and `#325` applied it.
  - **It exists.** It was created at 2026-10-09T17:09:49Z, and the PM read it back with `aws iam get-role` on 2026-10-10 (#320). The `garamsh-` name an earlier draft used was never applied (ADR 0051, Errata).
  - **Its trust admits a `v*` tag and nothing else.** `StringLike` `sub` is `repo:garamsh@307152666/garam-agent-operator@1335647420:ref:refs/tags/v*`, with `aud` `sts.amazonaws.com`, so no branch and no pull request gets the credential. It may push to `garam/garam-agent-operator` and `garam/garam-agent-operator-control` only.
  - **Unproven until the first release tag.** No token has reached the role yet. The first `v*` tag pushed on a commit whose `release.yml` names it is what proves the subject, and a mismatch refuses the assume rather than granting anything.
  - **If that assume fails,** the CloudTrail `AssumeRoleWithWebIdentity` event, with its `errorCode` and the `sub` it presented, goes to `garamsh/infra`, rather than the workflow's error line.
- **A development image is published by hand, in these steps.** Whoever does it holds a credential that can write the account. No contributor has one by virtue of contributing, and `docker` is not present on every contributor's machine.
  - Authenticate to ECR for account `486152169996` in `ap-northeast-2`.
  - Check out the commit being published and confirm the tree is unmodified. Nothing downstream will check this.
  - Run `make build-images`, which builds both images for that commit as one `linux/amd64` manifest each, tagged locally `garam-agent-operator:<commit>` and `garam-agent-operator-control:<commit>`, and pushes nothing.
  - For each image, run `make verify-image-ref IMAGE_REF=<reference>` on `486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/<its development repository>:<12-hex abbreviated commit hash>`, then tag the image with that reference and push it. Push no `dev-` tag (ADR 0067). Its development repository is `garam-dev/garam-agent-operator` or `garam-dev/garam-agent-operator-control`, never a repository under `garam/`. Until infra has created them, there is nowhere to push.
  - Read each digest back from the registry, not from the build's output: the registry is what a deployment pulls from, so only it shows the image is there. Give the deployment the tag and that digest together. Which repository owns the value that carries it is `configuration.md`'s; this document says what the value must look like.
- **A release is cut from `main`** (`integration.md`). After a promotion, a person tags `main`'s tip `vX.Y.Z` and pushes the tag, and the workflow publishes it. Not every promotion is released, and a tag on a commit only `dev` holds is refused.
- **The hand procedure has been run end to end, for the manager, into `garam/garam-agent-operator`, before ADR 0066 moved development images to `garam-dev/`.** The prior `garam/gagent-operator-dev` (18 images) and `garam/gagent-operator` (empty) repositories were emptied and removed on 2026-09-25. The release workflow has not run yet.
- A tag already published cannot be published again: the repository refuses the second push of a name rather than overwriting it. The repair for a bad image is a new commit or a new version, not a re-push.

- **This project's workloads land in every namespace that holds an `Agent`, and that set is unbounded on purpose.** `config/rbac/role.yaml` is a ClusterRole bound cluster-wide, so a StatefulSet is built wherever an `Agent` is created. The one namespace `config/` names — `garam-agent-operator-system`, in `config/default/kustomization.yaml` — is where the manager runs, not the limit of where its output goes.
- **A namespace holding such an `Agent` is this project's to answer for**, whether or not this project created the namespace. The StatefulSet in it, the Pod under that, and the PersistentVolumeClaim the template creates are built by this operator and by nothing else. What is not this project's is the input: the `Agent` and the credential Secret a person creates.
- **A hand-written `Agent` is reconciled wherever it lives, so a change to the workload's shape reaches every namespace holding one**, whatever manages that namespace and whatever PodSecurity floor it enforces. Deploying the manager is the only step: no per-namespace step follows it, the manager updates each StatefulSet it reconciles to the shape it now builds, and nothing in this repository names the namespaces that change will reach.
- **This operator neither refuses nor reports an `Agent` in a namespace whose PodSecurity floor is below the workload's.** The workload does not depend on the floor: the Pod carries its own constraints — `runAsNonRoot` and `seccompProfile` at Pod level, and one security context on every container (`internal/controller/agent_statefulset.go`) — and they are the same in every namespace. Reading a namespace's labels is also a grant `config/rbac/role.yaml` does not hold. A workload that does depend on the floor is what would reopen this.
- **The manager's own namespace joins the set as soon as this operator constructs an agent.** A constructed agent is built there rather than where its definition came from, so that namespace holds agents as well as the manager.
- **What another project may read in such a namespace: workload shape** — Pod and StatefulSet specs, container images, and `Agent` status. **Not Secret values**, in these namespaces or any other: the credential Secret holds an agent's private key and no diagnostic needs it. Reads only; a write triggers a reconcile.
- **That is the whole of what this project puts outside `config/`**: the four image repositories above, and the namespaces above. Where the control service runs is not among them: its manifests are the environment repository's, under ADR 0017, as the manager's overlay is. The images this project names but does not build — the init container's, set in `config/manager/manager.yaml`, and the agent's, which `--agent-image` supplies — are `configuration.md`'s, and the registries holding them are not this project's. The Garam adapter's image, which this project is to place beside an agent, is `garam`'s as well: this project does not build or publish it.

## Rationale

That the reference is the digest with the tag beside it, and that the repository is immutable with a tag naming one commit, is [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md). It borrows the argument `sherlock` makes for its own four repositories (`sherlock@04ed05a:docs/architecture/adr/0020-immutable-image-repositories.md`), which names this project's repository only to exclude it, and states the ground on which the borrowed argument holds here — a different ground, because `sherlock` checks at build time that a tag names the tree it was built from and this project does not.

Which repository owns the value a deployment carries that reference in is [ADR 0017](adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md), which supersedes [ADR 0013](adr/0013-the-base-carries-what-every-deployment-shares.md): the base keeps `controller:latest` as a replaceable name, the deploying overlay replaces it, and an environment nothing reconciles keeps that overlay here. That decides whose the value is; this document decides what it must look like.

That this project publishes a second image, the control service's, to a repository of its own is [ADR 0033](adr/0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md), following the service separation `garamsh/garam#1155` accepted as D1. It superseded only [ADR 0026](adr/0026-single-ecr-repository-dev-tag-by-role.md)'s one-image scope: ADR 0014's rules apply to each image unchanged. The two cannot share one repository because both images built from one commit claim the same commit tag, and an immutable repository refuses the second push.

That a release is a `vX.Y.Z` tag on `main`, published as `X.Y.Z` by a release workflow under a GitHub OIDC push role, with every image one `linux/amd64` manifest and development images pushed by hand under the commit hash, is [ADR 0051](adr/0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md), the owner's decision on 2026-10-06 (#268). It supersedes ADR 0014's index digest and its commit hash as a release's tag, the release tag of ADR 0026 and ADR 0033, and ADR 0015's publishing as what advances `main`.

That development images go to repositories under `garam-dev/` and `garam/` holds releases only is [ADR 0066](adr/0066-push-development-images-to-garam-dev-and-keep-garam-for-releases.md), the owner's directive of 2026-10-09 (#323). It supersedes ADR 0026's single repository per image, with the role carried by the tag, and ADR 0033's single repository per image for development builds. The tags stay as ADR 0051 sets them.

That a development image carries its hash tag alone, and that every push is checked against the two namespaces, is [ADR 0067](adr/0067-tag-a-development-image-with-its-commit-hash-alone-and-refuse-a-push-the-namespaces-forbid.md), settled on #323 and #325. It supersedes ADR 0066's unchanged tags and ADR 0051's `dev-` tag: the namespace marks the role, no reader uses the tag, and in an immutable repository a tag can be added later but never removed. `gitops` asked for the check at the source because it sees a development push to the wrong path only as `ImagePullBackOff`.

That a constructed agent is built in the manager's own namespace is [ADR 0009](adr/0009-construct-a-claimed-agent-from-the-operators-own-configuration.md), which is why that namespace is in the delivery set rather than beside it.

That the image carries a revision label was decided on issue #160, after a downstream repository that cites the deployed commit found the image carried no label. It changes no rule, so it carries no ADR.

Who may publish is not a decision this project makes. Which principals the account admits is `garamsh/infra`'s.

Nothing about the delivery set is itself a decision. The ClusterRole already settles which namespaces this operator reaches, and naming the set records what that grant already means.

That a hand-written `Agent` carries a change to the workload into whatever namespace holds it, and that the operator does not check that namespace's PodSecurity floor, was decided on issue #161, after a change to the Pod reached a namespace no repository manages with nothing naming it. The first records what the code already does and the second leaves the code as it is, so neither carries an ADR.

## Open questions

- **What put `gagent-bringup` there is not recorded anywhere.** Read-only on `admin@garam-dev` on 2026-08-31, two namespaces held an `Agent`: `gagent-bringup`, with `agent-sample` and the StatefulSet, Pod, PersistentVolumeClaim and credential Secret that go with it, and `gagent-operator-system`, with one constructed agent beside the manager. The second was `config/default`'s own namespace then; #168 renamed that namespace to `garam-agent-operator-system`. The first is named in this repository only as a path `garamsh/gitops` did not hold, and `config/samples/agent_v1alpha1_agent.yaml` — which declares an `Agent` of that name and no namespace — is not among `config/default`'s resources, so which namespace it was applied into was the applier's choice and nothing records it. `gagent-bringup` itself is gone: `kubectl get ns gagent-bringup` on `admin@garam-dev` returned NotFound on 2026-09-28, and its removal is `garamsh/gitops`'s (`garamsh/gitops#261`).
- **Whether the manager's own Pod should pull at every start.** A digest reference cannot come to mean different bytes, so the orchestrator's default is safe against a moved tag. What it is not safe against is a reference that stops resolving — the failure an image has once the repository no longer holds it — which the default turns into a stale cache on whichever nodes hold one.
