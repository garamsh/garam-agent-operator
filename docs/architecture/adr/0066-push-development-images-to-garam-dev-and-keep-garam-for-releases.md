# ADR 0066: Push development images to repositories under `garam-dev/`, and keep `garam/` for releases

> Status: accepted; partially superseded by ADR-0067: the development tags
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #323. The owner directed on 2026-10-09 that development images live under `garam-dev/` and that `garam/` holds releases only, and that every project in the account be defined the same way. The PM verified the directive in the owner's own record, which #323 cites, and it is quoted on `garamsh/garam#1234`.

Two records decide where this project's images go today:

- **[ADR 0026](0026-single-ecr-repository-dev-tag-by-role.md)** decides one repository per image, holding bring-up builds and releases alike, with the role carried by the `dev-<hash>` tag rather than by the repository.
- **[ADR 0033](0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md)** adds the control service's image, each image "to an ECR repository of its own", under ADR 0026's rules.

[ADR 0051](0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md) has since taken the tags of both: a release is `X.Y.Z`, pushed by `.github/workflows/release.yml` to `garam/garam-agent-operator` and `garam/garam-agent-operator-control`, and a development image is `<12-hex>` and `dev-<12-hex>`, pushed by a person. What remains of ADR 0026 and ADR 0033's repository column is that both kinds share one repository per image.

The registry on the day, as #323 records it from account `486152169996` on 2026-10-09:

- Every tagged image in `garam/garam-agent-operator` and `garam/garam-agent-operator-control` is a development tag. No release has been published.
- The lab's running manager pins `garam/garam-agent-operator` at `7c216469476d`. Promotion #269 and garam's S8 N-1 runs reference the same tag in both repositories.
- No repository under `garam-dev/` exists. Creating one is `garamsh/infra`'s.

ADR 0026 argued that a second repository per image costs a second declaration in `garamsh/infra`, a second name to learn, and a second repository to empty on a rename. Re-examined against the directive, those costs are unchanged and are now paid: the owner's rule is account-wide, so the split by repository is the account's convention and not this project's choice.

## Decision

**A development image is pushed to its image's repository under `garam-dev/`, and a release to its repository under `garam/`.** Each image has one repository of each kind:

| Image | Release repository | Development repository |
|---|---|---|
| Manager | `garam/garam-agent-operator` | `garam-dev/garam-agent-operator` |
| Control service | `garam/garam-agent-operator-control` | `garam-dev/garam-agent-operator-control` |

- **`garam/` holds releases only.** Only `.github/workflows/release.yml` pushes there, as ADR 0051 decides, and the workflow is unchanged.
- **The development repositories** are in the same account, `486152169996`, and region, `ap-northeast-2`. Each is created immutable and never made mutable, as [ADR 0014](0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md) requires of every repository here. Creating them is `garamsh/infra`'s.
- **Tags are unchanged.** A development image is still tagged `<12-hex>` and `dev-<12-hex>`, and a release `X.Y.Z`, as ADR 0051 decides. The repository now says which role, as well as which image.
- **No development image is pushed to `garam/`,** including before the `garam-dev/` repositories exist.
- **The images already in `garam/`** are development images. #323 moves them: each is copied to `garam-dev/` by digest once infra has created the repositories, each referrer moves to the new path at the same digest, and only then is it removed from `garam/`. Copying and removing images is the PM's.

This supersedes:

- **ADR 0026's single repository per image**, with the role carried by the tag. Its one-image scope was already superseded by ADR 0033 and its release tag by ADR 0051, and its development tags are carried forward by ADR 0051's table, so nothing of its decision is left standing.
- **ADR 0033's repository per image, for development builds.** Its two images, their entry points and Dockerfiles, and its repository names for releases stand.

ADR 0014's rules are not superseded: they apply to each of the four repositories.

## Consequences

- A deployment that pins a development image finds it under `garam-dev/` at the same digest, once #323's move is done. A reference's repository changes; its tag and digest do not.
- A reference under `garam/` is a release. A reader tells the role from the path, without reading the tag.
- `garamsh/infra` declares two more repositories. Whether a push role reaches them is that repository's decision, as it is for the first two.
- The hand procedure in `delivery.md` names the `garam-dev/` repositories. Until they exist, it has nowhere to push.

## What this does not decide

- **Whether the `dev-` tag still earns its place.** It carried the role in a shared repository, and the repository now carries it. Dropping it changes ADR 0051's tag table, which is a decision of its own.

## Rejected alternatives

- **Keep one repository per image, with the role in the tag.** The owner's directive refuses it.
- **A suffix, `garam/garam-agent-operator-dev`.** The directive names the `garam-dev/` path. A suffix is also the shape ADR 0026 removed, under a name its own Errata had to correct.
