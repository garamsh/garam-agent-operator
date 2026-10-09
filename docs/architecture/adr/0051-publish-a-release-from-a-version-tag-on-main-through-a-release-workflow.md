# ADR 0051: Publish a release from a version tag on `main` through a release workflow, and every image as one linux/amd64 manifest

> Status: accepted; the development image's `dev-` tag superseded by ADR-0067
> Date: 2026-10-06

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #268. The owner decided on 2026-10-06 that a `vX.Y.Z` git tag publishes this project's two images as `X.Y.Z`, through a release workflow under a GitHub OIDC push role. Development images are still pushed by hand, under the abbreviated commit hash.

That is the shape both other projects in the account already use:

- `garamsh/sherlock@b3c05c2:.github/workflows/release.yml`;
- `garamsh/garam@9a3f8a6:.github/workflows/release.yml`, its ADR-0039.

They share:

- a `v*` tag trigger, with a `vX.Y.Z` guard before any credential;
- every image built before any is pushed;
- no concurrency group;
- the digest and media type read back with `aws ecr batch-get-image`;
- one manifest per image, not an index.

What this replaces:

- **[ADR 0014](0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md):** a tag is always the commit's abbreviated hash, and a deployed digest is an image index's.
- **[ADR 0026](0026-single-ecr-repository-dev-tag-by-role.md) and [ADR 0033](0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md):** a release publish pushes only the commit-hash tag, for each image.
- **[ADR 0015](0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md):** "the trigger is publishing". `main` advances only at a commit a human published, and the promotion is a step in the publish sequence.

The facts on the day:

- **The control service's repository,** `garam/garam-agent-operator-control`, exists, IMMUTABLE, applied by `garamsh/infra` on 2026-10-05.
- **The push role,** `arn:aws:iam::486152169996:role/garam-agent-operator-github-actions`, is approved and not applied (garamsh/infra#292, renamed by #293 because roles are named for the subject that assumes them). It is to trust the subject `repo:garamsh@307152666/garam-agent-operator@1335647420:ref:refs/tags/v*`.

The PM settled the points the issue left open on the dispatch.

## Decision

**A release is a `vX.Y.Z` tag on `main`.**

- **When it is placed.** It goes on `main`'s tip after the promotion that put the commit there, which merges `dev` into `main` without squashing, as before.
- **Where it may go.** Never on a commit only `dev` holds.
- **How often.** Not every promotion is released.
- **Who places it.** A person pushes the tag. Nothing creates one.

**`.github/workflows/release.yml` publishes it**, modelled on sherlock's and run by the `v*` tag push alone. In order:

1. It refuses a tag that is not `vX.Y.Z`. It also refuses one whose commit is not an ancestor of `origin/main` (`git merge-base --is-ancestor`). Both checks come before any credential, so a refused tag costs nothing.
2. It builds both images through one entry point, `make build-images REVISION=<commit>`, which pushes nothing.
3. Only then does it assume `garam-agent-operator-github-actions`.
4. It pushes the manager's image to `garam/garam-agent-operator:X.Y.Z` and the control service's to `garam/garam-agent-operator-control:X.Y.Z`.
5. It reads each back with `aws ecr batch-get-image`. It refuses a tag that resolves to nothing, or to an index, and prints `repository:X.Y.Z@sha256:<digest>` to the step summary.

The workflow also has these properties:

- **No concurrency group.**
- **`permissions: read-all` at the top**, and `contents: read` with `id-token: write` on its one job.
- **Actions pinned by SHA** with the version beside each, moved by the existing `github-actions` Dependabot entry.

**Every image this project publishes, release or development, is one `linux/amd64` manifest.** This supersedes ADR 0014's "the digest is the index's".

- **How it is built.** `build-images` builds with `--platform=linux/amd64` on the default driver, with buildx's default attestations off. The digest a deployment pins therefore names the image, and the platform is stated rather than pinned silently.
- **Why.** One shape across the account is worth more than an index nobody pulls for a second architecture.
- **The last images of the old shape.** The development images pushed by hand on 2026-10-06 under `7c216469476d` are buildx indexes under the old rule.

**Tags.** This supersedes the tag parts of ADR 0014, ADR 0026 and ADR 0033:

| Image | Tags | Pushed by |
|---|---|---|
| Release | `X.Y.Z` only | the release workflow |
| Development | `<12-hex abbreviated commit hash>` and `dev-<12-hex>` | a person, by `delivery.md`'s procedure |

- **Unchanged:** every repository stays immutable, with no `latest` and no moving tag. A deployment still references `<repository>:<tag>@sha256:<digest>`.
- **Which commit a release is.** The git tag `vX.Y.Z` names it, and the image's `org.opencontainers.image.revision` label carries its full hash.

**`main` advances only by a promotion.** This supersedes ADR 0015's "the trigger is publishing": publishing no longer advances `main`. A release is the tag a person places after one. ADR 0015's no-squash rule stands, for a new reason: a squash would mint a commit no `vX.Y.Z` tag and no development image's hash tag names.

**No release tag is pushed until the push role exists.** A run before then fails at the credential step having pushed nothing, because the build comes first.

- **Half-published.** Both repositories exist, and a run whose second push fails leaves `X.Y.Z` half-published. As in sherlock and garam, only the next version repairs that.
- **No pre-check of the repositories.** The workflow does not describe them first, because that would widen the role beyond what a push needs.

## Consequences

- **A release no longer needs an account credential on a person's machine,** and every release is built from a tree the runner checked out at the tag, not from a contributor's working copy.
- **The tag on `main` is the record of a release.** `git show vX.Y.Z` answers which commit, where a commit hash tag used to.
- **The workflow cannot run here.** It is checked by `make verify-pins` and `actionlint` alone. Its first real run is verified three ways:
  - the run's step summary carries both `repository:X.Y.Z@sha256:…` lines;
  - the owner's `aws ecr batch-get-image` shows each digest with a single-manifest media type;
  - each image's `org.opencontainers.image.revision` label equals `git rev-parse vX.Y.Z`.
- **Development images keep the hand procedure,** so a development build still rests on its pusher's tree being clean. Immutability still bounds that to one push per name.
- **A multi-architecture image is not published.** A second architecture is a new decision, not a flag.

## Errata

### 2026-10-08 — the push role is `garamsh-garam-agent-operator-github-actions`

Decision names the push role `arn:aws:iam::486152169996:role/garam-agent-operator-github-actions`, and its third step assumes `garam-agent-operator-github-actions`. infra applied an owner-approved naming scheme that puts the forge owner in a role's name: infra-a5 reported the applied names `garamsh-garam-github-actions` and `garamsh-sherlock-github-actions` on 2026-10-08 (#320). That scheme names this repository's push role `arn:aws:iam::486152169996:role/garamsh-garam-agent-operator-github-actions`, which `release.yml` and `delivery.md` now name. The role is still to be applied under `garamsh/infra#292`. The decision, the subject the role is to trust, and the rule that no release tag is pushed before the role exists all stand.

### 2026-10-10 — the push role is `garam-agent-operator-github-actions` again, and it exists

The 2026-10-08 entry above was wrong. `garamsh/infra#303` retired the `garamsh-` prefix, and `garamsh/infra#293` names a push role `<repository>-github-actions`, so the name `garamsh-garam-agent-operator-github-actions` was never applied. It returns `NoSuchEntity`.

The role is the one Decision names, `arn:aws:iam::486152169996:role/garam-agent-operator-github-actions`. It was created at 2026-10-09T17:09:49Z (`garamsh/infra#292`, delivered by `#325`), and the PM read it back on 2026-10-10 (#320). Its trust admits only the subject `repo:garamsh@307152666/garam-agent-operator@1335647420:ref:refs/tags/v*`. `release.yml` and `delivery.md` name it again.

The decision and the subject stand. The first `v*` tag is what proves the subject.
