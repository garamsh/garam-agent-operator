# ADR 0026 — Single ECR repository, dev tag by role

## Status

Accepted. Settled on issue #169's follow-up after the rename work and the
sherlock rename closed and the operator's image was first published under the
new name. Extends ADR 0014, supersedes nothing.

## Context

ADR 0014 settled the shape of every published tag: the abbreviated commit
hash, no `latest`, no moving tag, the repository created immutable. It did
not decide whether bring-up builds and releases should live in one
repository or two. The repository that carried bring-up builds was named
`garam/garam-agent-operator-dev`; the non-dev `garam/gagent-operator`
repository was created on 2026-09-05 and stayed empty.

The bring-up / release split into two repositories costs more than it buys:

- A second ECR repository is one more thing the `garamsh/infra` module
  declares and one more thing `garamsh/gitops` references, with no
  immutability, no tag policy and no scan-on-push configuration that the
  first repository does not already provide. The same policy applied
  twice is policy duplicated, not policy stronger.
- A second repository is one more name a reader has to learn. A bring-up
  deployment named `garam/garam-agent-operator-dev:<sha>` and a release
  deployment named `garam/gagent-operator:<sha>` differ in role, not in
  bytes; the role is what the `-dev` suffix carries, and a suffix is a
  weaker signal than a tag.
- A second repository is one more repository to empty on a rename.
  `garam/gagent-operator-dev` had eighteen images by the time this ADR was
  settled, and they had to be deleted and the repository itself deleted
  alongside it. The non-dev `garam/gagent-operator` was empty and was
  deleted for the same reason.

The single-repository alternative keeps ADR 0014's per-tag guarantees —
immutability, scan-on-push, abbreviated-commit-hash tag — and adds a
second tag of the form `dev-<hash>` on the same image when the build is
for bring-up rather than a release.

## Decision

The operator publishes one image, to one ECR repository, with two kinds
of tag.

| Tag | What it identifies |
|---|---|
| `<abbreviated commit hash>` | The image built from that commit. ADR 0014's identifier. |
| `dev-<abbreviated commit hash>` | The same image, marked as a bring-up build rather than a release. |

Both tags point to the same image. The commit-hash tag is the identifier;
the `dev-` tag is the role. Both are immutable; both are pushed on every
publish that is for bring-up. A release publish pushes only the
commit-hash tag.

The repository is `garam/garam-agent-operator` in AWS account
`486152169996`, region `ap-northeast-2`. It is created immutable, with
scan-on-push, and never made mutable. ADR 0014's other guarantees — no
`latest`, no moving tag, deployment references the image by digest with
the tag beside it — carry forward unchanged.

The prior `garam/garam-agent-operator-dev` and `garam/gagent-operator`
repositories are emptied and removed. Their images, when they existed,
are deleted alongside the repository.

## Consequences

- One ECR repository instead of two. One `aws_ecr_repository`
  declaration in `garamsh/infra`, one scan-on-push setting, one
  immutability setting, one set of access policies. The bring-up / release
  split lives in the tag, not in the repository name.
- A reader of the cluster resolves the image from the tag. A bring-up
  deployment named `garam/garam-agent-operator:dev-6175a02` and a release
  deployment named `garam/garam-agent-operator:6175a02` are the same image
  with different role tags; `git show 6175a02` works for both, and
  `describe-images --image-ids imageTag=6175a02` returns the same digest
  the deployment carried.
- A rename of the project is a rename of the repository. The tag policy
  does not change. ADR 0014's per-tag guarantees apply to both tags
  identically, so a future tag shape (a release-specific prefix, for
  example) lands as another row of the same table, not as a second
  repository.

## What this does not decide

- The release path. ADR 0014's open question — "There is no release
  repository and no release path" — is reframed, not closed. There is
  still no release path; there is a release tag shape that lands when one
  exists.
- The `release-` prefix. A release publish today carries only the
  commit-hash tag; if a release-specific prefix lands, this ADR is
  amended or a successor is appended.