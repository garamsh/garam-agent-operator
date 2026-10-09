# ADR 0067: Tag a development image with its commit hash alone, and refuse a push the namespaces forbid

> Status: accepted
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0066](0066-push-development-images-to-garam-dev-and-keep-garam-for-releases.md) moved development images to repositories under `garam-dev/` and kept `garam/` for releases. It left two things as they were:

- **The tags.** "Tags are unchanged": a development image was still tagged `<12-hex>` and `dev-<12-hex>`, as [ADR 0051](0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md)'s table has it. ADR 0066 named whether the `dev-` tag still earns its place as a question it did not decide.
- **Enforcement.** Nothing refused a development push to `garam/`. `garamsh/gitops` reported on #323 that it sees a dead development path only as `ImagePullBackOff`, and suggested the refusal belong at the source. #325 asks for it.

The PM settled the tags on #323 (2026-10-09), when infra's review of the copy to `garam-dev/` asked which tags to copy:

- **The namespace already marks the role.** The `dev-` prefix existed (ADR 0026) to mark a development build inside a repository shared with releases. Under `garam-dev/`, nothing is shared.
- **No reader uses it.** `garamsh/gitops` pins by digest beside the hash tag, and garam's harness uses digests.
- **Fewer tags is the reversible direction.** Every repository here is immutable, so a tag can be added later and never removed.

So infra copies each image to `garam-dev/` under its hash tag only.

## Decision

**A development image under `garam-dev/` is tagged with the 12-hex abbreviated hash of the commit built, and with nothing else.** There is no `dev-` tag. Releases keep `X.Y.Z` under `garam/`, as ADR 0051 decides.

**Every path that pushes an image refuses a reference the two namespaces forbid**, through one check, `hack/imageref`, run as `make verify-image-ref IMAGE_REF=<reference>`:

| Reference | Verdict |
|---|---|
| Under `garam/`, tagged `X.Y.Z`, in `release.yml`'s run for the tag `vX.Y.Z` | accepted |
| Under `garam/`, anything else | refused, naming `garam-dev/` and ADR 0066 |
| Under `garam-dev/`, with a `dev-` tag or no tag | refused, naming ADR 0067 |
| Under `garam-dev/`, any other tag | accepted |
| Under neither | no rule applies |

- **Where it runs.** Every Makefile target that pushes, `docker-push` and `docker-buildx`, runs it before anything else. `release.yml` runs it on each reference before tagging. The hand procedure in `delivery.md` runs it before each push.
- **How a release is recognized.** By `GITHUB_WORKFLOW_REF`, which the Actions runner sets to the workflow file and the ref that started the run: `garamsh/garam-agent-operator/.github/workflows/release.yml@refs/tags/vX.Y.Z`. The tag pushed must be that ref's version. The workflow sets nothing for it, so a release proves it is one through a value it does not write.
- **How a reference is read.** With `github.com/distribution/reference`, the parser the docker CLI applies, so the check and the push agree on what the repository is.

**Its limit.** The check stops mistakes, not a determined pusher. A local shell that exports `GITHUB_WORKFLOW_REF` by hand passes, and a person who pushes with `docker` directly never runs it. What stops the rest is IAM: which principals may write to `garam/` is `garamsh/infra`'s.

This supersedes:

- **ADR 0066's "Tags are unchanged"**, and answers the question it left open.
- **ADR 0051's development tags**, `<12-hex>` and `dev-<12-hex>`, for the `dev-` tag. Its release tags and everything else in it stand.

## Consequences

- A development reference is `garam-dev/<repository>:<12-hex>@sha256:<digest>`, and `git show <12-hex>` still takes its tag.
- The `dev-` tags already in `garam/` are not copied to `garam-dev/`. They leave with the images when #323 removes them from `garam/`.
- A typo or a stale default that names `garam/` for a development push fails before anything is built or pushed, with a message naming the `garam-dev/` reference to use instead.
- The check does not verify that a hash tag names the commit built. That gap is `delivery.md`'s, and is unchanged.

## Rejected alternatives

- **Keep the `dev-` tag under `garam-dev/`.** It marks a role the namespace already marks, and once pushed it can never be removed.
- **A variable `release.yml` sets, such as `RELEASE=1`.** Any shell sets it as easily. `GITHUB_WORKFLOW_REF` at least ties the exemption to the release workflow's own file and the tag being released, and needs no line in the workflow to keep.
- **The check in the shell of each target.** Each target would carry its own copy, and none would be tested. One program, unit-tested in `make ci`, is the rule once.
