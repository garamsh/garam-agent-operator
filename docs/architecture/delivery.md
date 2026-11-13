# Delivery

What this project publishes, where its output lands in a cluster, and who owns each of those places.

## Current decisions

- This project builds and publishes one image, the control service's, to one ECR repository of its own in AWS account `486152169996`, region `ap-northeast-2`. The repository is named `garam-agent-operator/<image>` and holds the image's release and development images, told apart by tag (ADR 0078). This project publishes no other image and reaches no other registry. It builds the gaon operator's two images and publishes neither, as `gaon-operator.md` records.

| Image | Entry point | Dockerfile | Repository |
|---|---|---|---|
| Control service | `cmd/control/main.go` | `build/control.Dockerfile` | `garam-agent-operator/control` |

- **The repository is `garamsh/aurora#711`'s to create, and it was open on 2026-10-30.** Creating any repository is `garamsh/aurora`'s. This document names where the image goes; it does not say the repository exists. No `v*` tag is pushed until it does, because `release.yml` fails on a repository that is missing.
- **`garamsh/aurora#711` moves what is already published.** It copies each tag by digest, so a pin keeps its tag and digest and changes only its repository, then repoints the references and retires the old repositories under `garam/` and `garam-dev/` with the owner's approval. Those repositories hold every image published so far until the copy is done.
- **The repository says which image, and the tag says which role and which commit.** Every rule below applies to the image and its repository.
- That account is not this project's to set, so what follows states what this project requires of the repository. Nothing here describes a setting the account holds: a description of another account goes stale with nothing in this repository able to notice.
- **Each repository is created immutable and is never made mutable.** A tag identifies a build only where a second push of that name is refused. The setting is retroactive in effect — it is a property of the repository, not of a tag, and no tag records which policy it was pushed under — so a repository that is ever mutable leaves every tag it holds a label rather than an identifier, including tags pushed while it was immutable.
- **A release image is tagged with its version; a development image is tagged with the abbreviated commit hash of the commit built** (ADR 0051). Until launch a version is `0.1.0-dev.N` or the candidate `0.1.0-rc.N`, where N counts from 1 and never repeats, and `0.1.0` is cut at launch (ADR 0081). Every other version is refused. The two forms cannot collide: a version tag carries dots and a hash tag none (ADR 0078).

| Image | Tags | Pushed by |
|---|---|---|
| Release | `0.1.0-dev.N`, `0.1.0-rc.N` or `0.1.0`, from the git tag of the same name with a `v` in front, the `v` stripped | `.github/workflows/release.yml` |
| Development | `<12-hex abbreviated commit hash>` only (ADR 0067) | a person, by the procedure below |

  - **No moving tags.** There is no `latest` and no moving tag.
  - **Development tags.** The commit tag is the identifier, and its form marks the role, so there is no `dev-` tag (ADR 0067, ADR 0078).
  - **Expiry.** A release tag never expires and never moves. Which tags expire, and whether a repository's tags are mutable, is `garamsh/aurora`'s: `garamsh/aurora#711` for the repositories, and for the `0.1.0-dev.N` and `0.1.0-rc.N` forms its ADR 0033, which `garamsh/aurora` is changing under decision 14 of `garamsh/aurora#749`. This project requires of it that nothing selecting development tags matches a version tag.
  - **A release's commit.** The git tag `v<version>` names it, and the image's revision label carries its full hash.
- **Every push is checked against the tag rule first** (ADR 0078, #401). `make verify-image-ref IMAGE_REF=<reference>` runs `hack/imageref`. Under `garam-agent-operator/`, it accepts a release version only in `release.yml`'s run for the git tag `v<version>`, as `GITHUB_WORKFLOW_REF` names it, accepts the 12-hex commit hash alone, and refuses everything else, a `dev-` tag and no tag included. Outside `garam-agent-operator/` no rule applies, so it does not refuse a stale reference to a repository this project has left. `release.yml` runs it before its push. It stops mistakes, not a determined pusher: a shell can export `GITHUB_WORKFLOW_REF`, and a bare `docker push` never runs it. Which principals may write to these repositories is `garamsh/aurora`'s.
- **Nothing in this repository checks that a development tag tells the truth.** No `Makefile` target compares the build context against the commit, so a development tag is a claim its pusher typed, and an image built from a modified tree carries a clean commit's name with nothing marking it. Immutability is what bounds that: it holds for the first push of a name and refuses every later one. A release is built by the workflow from the tree it checked out at the tag, so the same gap does not arise there.
- **The image builds from the repository root through one `.dockerignore`**, which ignores everything and re-includes the Go source, the module files and every file Go source embeds. There is no per-Dockerfile ignore file. A file a package embeds that the ignore file leaves out stops `go build` inside the image build only, which `make ci` and the e2e suite, building outside Docker, never reach (#301). So `make ci` runs `make verify-build-context`, which checks every package's `//go:embed` files against the ignore file with `moby/patternmatcher`, the matcher BuildKit applies (`hack/buildcontext`), and fails naming each one left out.
- **The image carries the commit it was built from as the label `org.opencontainers.image.revision`**, beside `org.opencontainers.image.source` and `org.opencontainers.image.title`. `build/control.Dockerfile` writes the three from `ARG REVISION`. `make build-images` passes `REVISION`, which defaults to `git rev-parse HEAD`, and so does `make docker-build-control`.
- **The label is a claim the builder typed, as the tag is.** `--build-arg` accepts any string, and a modified tree is labelled with the clean commit it was checked out from. What the label adds is who can read the claim: a consumer holding a deployed reference reads it off the image in the registry, with no person asked. It is not a check against the tree.
- **A deployment references the image by digest and carries the tag beside it**, as `<repository>:<tag>@sha256:<digest>`. Both halves are required and each answers a question the other cannot:

| Half | Answers | Why it cannot be dropped |
|---|---|---|
| Digest | Which bytes | It is the only half a runtime resolves, and it rests on nothing a consumer would have to trust. A tag's stability rests on a repository setting, and a consumer can read that setting today but not the repository's history. |
| Tag | Which source tree | A reader of the cluster gets the commit without a registry round trip, and `git show` takes it: the hash for a development image, `v<tag>` for a release. Left out, every deployed reference is opaque to a reader with no access to the account — which is this document's own subject, moved from the repository into the cluster. |

- **Every published image is one `linux/amd64` manifest, and the digest a deployment carries is that manifest's** (ADR 0051, superseding ADR 0014's index digest). `make build-images` builds the image with `--platform=linux/amd64` and with buildx's default attestations off, so it is not wrapped in an index, and the platform is stated here rather than pinned silently. The development images pushed by hand under `7c216469476d` on 2026-10-06 are the last of the old shape, buildx indexes.

- **A release is published by `.github/workflows/release.yml`**, on the push of a `v*` tag and nothing else, modelled on `garamsh/sherlock@b3c05c2:.github/workflows/release.yml`. In order, it:
  - refuses a tag that is not `v0.1.0-dev.N`, `v0.1.0-rc.N` or `v0.1.0`, or whose commit is not on `main`, before any credential, the tag check being `hack/releasetag`, which `hack/releaseversion` decides for it and for `hack/imageref`;
  - builds the image through `make build-images`, which pushes nothing;
  - then assumes the push role;
  - pushes the image as the version, the tag without its `v`;
  - reads it back from the registry with `aws ecr batch-get-image`, refusing a missing image or an index;
  - prints `repository:<version>@sha256:<digest>` to the step summary, which is where a deployment's reference comes from.

  It has no concurrency group, so a queued release is never cancelled. A run whose second push fails leaves the version half-published, which only the next version repairs.
- **The release workflow pushes as `arn:aws:iam::486152169996:role/github-actions-garam-agent-operator-push`.** Its name, permissions and trust are `garamsh/aurora`'s: `#292` requested the role, `#325` applied it as `garam-agent-operator-github-actions`, and the owner's 2026-10-16 naming rule (`garamsh/infra` `442e19f`, a forge identity is named `github-actions-<repository>`, with a purpose segment here because this repository holds two roles) renamed it. Infra created the new role beside the old one (`garamsh/infra#450`, #372). The old push role, `garam-agent-operator-github-actions`, was removed with the account's other superseded push roles on 2026-10-19 (`garamsh/aurora#654`, applied by `garamsh/aurora#657`); `get-role` on it returns `NoSuchEntity`, read by the PM on 2026-10-19.
  - **It exists.** The PM read it back from IAM on 2026-10-16 (#372): its trust policy and its inline policy `ecr-push` are byte-identical to the old role's, and neither has an attached policy. The `garamsh-` name an earlier draft used was never applied (ADR 0051, Errata).
  - **Its trust admits a `v*` tag and nothing else.** `StringLike` `sub` is `repo:garamsh@307152666/garam-agent-operator@1335647420:ref:refs/tags/v*`, with `aud` `sts.amazonaws.com`, so no branch and no pull request gets the credential. It pushes to the repository above once `garamsh/aurora#711` moves its `repository_names` to it; its subject does not change.
  - **Proven by a release tag, and by every one after it.** A `v*` tag pushed on a commit whose `release.yml` names it is what proves the subject, and a mismatch refuses the assume rather than granting anything. Three tags have proved it: `v0.1.0-dev.1`, `v0.1.0-dev.2` and `v0.1.0-dev.3`, published under the names `v0.1.0`, `v0.2.0` and `v0.3.0` and renamed under ADR 0081, each on a commit `main` held, and each run ended `success`. A run that could not assume the role could not have pushed, so a successful one says the credential reached it.
  - **If that assume fails,** the CloudTrail `AssumeRoleWithWebIdentity` event, with its `errorCode` and the `sub` it presented, goes to `garamsh/aurora`, rather than the workflow's error line.
- **A development image is published by hand, in these steps.** Whoever does it holds a credential that can write the account, under their own identity: the push role admits only a `v*` tag. No contributor has one by virtue of contributing, and `docker` is not present on every contributor's machine.
  - **Until `garamsh/aurora#711` is applied, this procedure cannot be completed.** The build and `make verify-image-ref` run today. The push and the read-back need the repository to exist and a person's write into it.
  - Authenticate to ECR for account `486152169996` in `ap-northeast-2`.
  - Check out the commit being published and confirm the tree is unmodified. Nothing downstream will check this.
  - Run `make build-images`, which builds the image for that commit as one `linux/amd64` manifest, tagged locally `garam-agent-operator/control:<commit>`, and pushes nothing.
  - Run `make verify-image-ref IMAGE_REF=<reference>` on `486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/garam-agent-operator/control:<12-hex abbreviated commit hash>`, then tag the image with that reference and push it. Push no `dev-` tag (ADR 0067), and no version tag: that form is the release workflow's.
  - Read the digest back from the registry, not from the build's output: the registry is what a deployment pulls from, so only it shows the image is there. Give the deployment the tag and that digest together. This document says what the value must look like.
- **A release is cut from `main`** (`integration.md`). After a promotion, a person tags `main`'s tip `v0.1.0-dev.N`, `v0.1.0-rc.N` or, at launch, `v0.1.0` and pushes the tag, and the workflow publishes it. The next release is `v0.1.0-dev.5`. Not every promotion is released, and a tag on a commit only `dev` holds is refused. The workflow does not create the GitHub release: the person who pushes the tag creates it, marked pre-release for `-dev` and `-rc` (ADR 0081).
- **Neither path has run against the repository above.** The hand procedure was run end to end before ADR 0066 split the repositories. The prior `garam/gagent-operator-dev` (18 images) and `garam/gagent-operator` (empty) repositories were emptied and removed on 2026-09-25. The release workflow has run three times, for the tags now named `v0.1.0-dev.1`, `v0.1.0-dev.2` and `v0.1.0-dev.3`, each on a commit `main` held (`d6a7e76`, `dbe85d4`, `f3841de`). Each run ended `success` after pushing its images to repositories under `garam/` and reading each digest back from the registry, so what those repositories hold is one `linux/amd64` manifest under each tag. The workflow's first run against the repository above is the next release after it exists.
- A tag already published cannot be published again: the repository refuses the second push of a name rather than overwriting it. The repair for a bad image is a new commit or a new version, not a re-push.

- **That is the whole of what this project publishes**: the one image repository above. Where the control service runs is not among it: its manifests are the environment repository's. The Garam adapter's image and the agent's are `garam`'s and `sherlock`'s, and this project builds and publishes neither.

## Rationale

That the reference is the digest with the tag beside it, and that the repository is immutable with a tag naming one commit, is [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md). It borrows the argument `sherlock` makes for its own four repositories (`sherlock@04ed05a:docs/architecture/adr/0020-immutable-image-repositories.md`), which names this project's repository only to exclude it, and states the ground on which the borrowed argument holds here — a different ground, because `sherlock` checks at build time that a tag names the tree it was built from and this project does not.

That this project publishes the control service's image to a repository of its own is [ADR 0033](adr/0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md), following the service separation `garamsh/garam#1155` accepted as D1. It superseded only [ADR 0026](adr/0026-single-ecr-repository-dev-tag-by-role.md)'s one-image scope, and the image was the second beside the manager's until [ADR 0083](adr/0083-remove-the-kubernetes-manager.md) removed the manager and left it the only one. ADR 0014's rules apply to the image unchanged.

That a release is a version tag on `main`, published under that version without the `v` by a release workflow under a GitHub OIDC push role, with every image one `linux/amd64` manifest and development images pushed by hand under the commit hash, is [ADR 0051](adr/0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md), the owner's decision on 2026-10-06 (#268). It supersedes ADR 0014's index digest and its commit hash as a release's tag, the release tag of ADR 0026 and ADR 0033, and ADR 0015's publishing as what advances `main`.

That a release's version is `0.1.0-dev.N`, `0.1.0-rc.N` or, at launch, `0.1.0`, and that no other tag publishes, is [ADR 0081](adr/0081-release-as-0-1-0-dev-n-and-0-1-0-rc-n-until-launch-and-0-1-0-at-launch.md), from the owner's decision 14 on `garamsh/aurora#749` (#414). It supersedes only the version form of ADR 0051 and ADR 0078.

That each image has one repository, named `garam-agent-operator/<image>`, holding release and development images told apart by tag, is [ADR 0078](adr/0078-publish-each-image-to-one-repository-and-tell-release-from-development-by-tag.md), from the owner's decisions of 2026-10-28 (#401, `garamsh/aurora#709`, `garamsh/aurora#711`). It supersedes [ADR 0066](adr/0066-push-development-images-to-garam-dev-and-keep-garam-for-releases.md), which had split each image into a repository under `garam/` and one under `garam-dev/`, the repository names in ADR 0033 and ADR 0051, and [ADR 0067](adr/0067-tag-a-development-image-with-its-commit-hash-alone-and-refuse-a-push-the-namespaces-forbid.md)'s refusal by namespace. `hack/imageref` therefore decides by the tag.

That a development image carries its hash tag alone, and that every push is checked by one program, is ADR 0067, settled on #323 and #325. It supersedes ADR 0051's `dev-` tag: no reader uses the tag, and in an immutable repository a tag can be added later but never removed. `gitops` asked for the check at the source because it sees a development push to the wrong path only as `ImagePullBackOff`.

That the image carries a revision label was decided on issue #160, after a downstream repository that cites the deployed commit found the image carried no label. It changes no rule, so it carries no ADR.

Who may publish is not a decision this project makes. Which principals the account admits is `garamsh/aurora`'s.

## Open questions

None.
