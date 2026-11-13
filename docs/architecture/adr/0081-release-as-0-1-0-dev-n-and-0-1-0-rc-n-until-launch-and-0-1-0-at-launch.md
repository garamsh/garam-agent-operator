# ADR 0081: Release as 0.1.0-dev.N and 0.1.0-rc.N until launch, and 0.1.0 at launch

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #414. The owner decided, as decision 14 on `garamsh/aurora#749` (issuecomment-6846684552) under the organization's policy `garamsh/discussions#4`:

- Every release before launch is `0.1.0-dev.N`. N counts from 1 and never repeats.
- A rehearsal candidate is `0.1.0-rc.N`.
- `0.1.0` is cut at launch, on the commit that passed `garamsh/aurora#749`'s four rehearsals.

The existing releases are already renamed (`garamsh/aurora#749`, issuecomment-6848195101). The git tags are `v0.1.0-dev.1` to `v0.1.0-dev.4`, at the commits of the former `v0.1.0` to `v0.4.0`, and the three releases were recreated as pre-releases under the new names. The next release is `v0.1.0-dev.5`.

What the release path accepted before this decision, and so refused every one of these:

- **[ADR 0051](0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md)** made a release a `vX.Y.Z` tag, published as `X.Y.Z`, and `release.yml` refused any other `v*` tag.
- **[ADR 0078](0078-publish-each-image-to-one-repository-and-tell-release-from-development-by-tag.md)** had `hack/imageref` accept `X.Y.Z` as a release image tag in `release.yml`'s run for `vX.Y.Z`.

The push role trusts the subject `refs/tags/v*`, and all three new forms are under it, so nothing changes in `garamsh/aurora` or `garamsh/infra` for the role. The ECR lifecycle and tag mutability for these forms are `garamsh/aurora`'s, which is changing them under the same decision through its own ADR 0033.

## Decision

1. **A release is one of three versions.** `0.1.0-dev.N` and `0.1.0-rc.N`, where N is a positive integer without a leading zero, and `0.1.0`. Its git tag is the version with a `v` in front, and its image tag is the version without it. Every other `v*` tag is refused, a bare `v0.2.0` and `v0.1.0-dev.0` included.

2. **One program decides the form, and the workflow and `hack/imageref` both call it.**
   - `hack/releaseversion` spells the three forms once.
   - `hack/releasetag` answers the version a tag names, or refuses it. `release.yml`'s version step runs it before any credential, with the tag in the environment and not in the command text.
   - `hack/imageref` takes its release-version test from `hack/releaseversion`, so the tag a push is checked for and the tag the workflow derives cannot drift. It still accepts the 12-hex commit hash alone in any run, and a release version only in `release.yml`'s run for the git tag of that version.

3. **The rest of the release path stands.** A release tag is placed on `main`'s tip after a promotion and is refused before any credential when its commit is not on `main`. Both images are built before either is pushed, and the registry is read back after the push.

4. **`release.yml` does not create the GitHub release.** The person who pushes the tag creates it, marked pre-release for `-dev` and `-rc`.
   - Creating a release needs `contents: write`. The job holding the federated push credential holds `contents: read` and writes nothing back to the repository, and `ci.md` §Security baseline grants a write on the single job that needs it, so a release step would need a second job holding only that grant.
   - That second job would be a second failure mode with its own ordering problem: before the push it announces a version that may not publish, and after it a failure leaves a published version with no release, repaired by hand, as a half-published version already is.
   - Nothing in this repository reads a GitHub release. The git tag names the commit and the registry holds the image, which is the record ADR 0051 relies on.

This supersedes the version form of:

- **ADR 0051**, `vX.Y.Z` for the git tag and `X.Y.Z` for the image tag.
- **ADR 0078**, `X.Y.Z` as the release image tag in decision 2 and in the table of decision 3.

It supersedes nothing else in them. ADR 0051's workflow, its order of steps, its single-manifest images and its push role stand, and so do ADR 0078's repositories, its hash tag, and `GITHUB_WORKFLOW_REF` narrowing a release tag and nothing else.

## Consequences

- **Until this lands no release can be cut,** because `release.yml` refuses `v0.1.0-dev.5`. After it, the next release is `v0.1.0-dev.5`.
- **The form is closed on purpose.** A release after `0.1.0` is a new decision, since the rule accepts no `0.1.1` or `0.2.0`.
- **The tag check is tested over names, and the workflow's order is tested too.** `hack/releasetag` tests accepted names beside near misses, and a test reads `release.yml` and fails when the check does not run before the credential step. The workflow itself cannot run here, so its first real run is the next release.
- **A tag the check refuses has cost nothing,** because the version step precedes the credential, and the repositories are immutable, so a wrong name published would be permanent.
- **Tag expiry for these forms is `garamsh/aurora`'s.** `delivery.md` names the dependency, and this project requires only that nothing selecting development tags matches a version tag.
- **The renamed tags are cited by their new names.** The three runs that proved the push role's subject ran under the former names; `delivery.md` and `integration.md` say so.

## Rejected alternatives

- **Keep `vX.Y.Z` and add the pre-release forms beside it.** The owner's decision refuses a bare version other than `0.1.0`, and an open form would accept `v0.2.0` before launch.
- **Spell the form again in the workflow's shell and in `hack/imageref`.** Two copies of one rule are what let a refused tag be pushed under a name the other check admits, and the shell copy cannot be tested directly.
- **Create the GitHub release in `release.yml`,** as codehub does (`garamsh/codehub#160`). Rejected for the reasons in decision 4, which hold for this project's workflow and make no claim about codehub's.
