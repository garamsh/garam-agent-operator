# ADR 0084: Publish the gaon operator and the guest image from the release workflow

> Status: superseded by ADR-0086
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #423, step 4 of #416. [ADR 0082](0082-import-garams-gaon-operator-and-keep-it-in-this-repository.md) imported the gaon operator and its guest image, built by `make operator-image` and `make guest-image` under the local names `garam-agent-operator/gaon-operator` and `garam-agent-operator/gaon-guest`, and published by nothing. [ADR 0083](0083-remove-the-kubernetes-manager.md) left the control service as the only image `release.yml` published.

`garamsh/aurora#790` (issuecomment-6850330918) created two more repositories, immutable: `garam-agent-operator/operator` and `garam-agent-operator/guest`. The push role `github-actions-garam-agent-operator-push` pushes to them and to `control`, and it reads `sherlock/agent`, `sherlock/workspace` and `garam/server`, the three source images `build/guest.Dockerfile` pins by digest. Its trust stays the subject `refs/tags/v*`.

What stood in the way:

- The guest image's sources are in a private registry, so a build needs a login. `release.yml` took its credential after the build, on the ground that every step before the push is reversible.
- [ADR 0078](0078-publish-each-image-to-one-repository-and-tell-release-from-development-by-tag.md)'s check, `hack/imageref`, applied its tag rule to every name under `garam-agent-operator/` and knew no repository. A push to a name nobody created, the old `gaon-operator` among them, would pass it and fail at the registry after the upload began.

## Decision

1. **A release publishes three images.** For one git tag `v<version>`, `release.yml` pushes `garam-agent-operator/operator:<version>`, `garam-agent-operator/guest:<version>` and `garam-agent-operator/control:<version>`, each one `linux/amd64` manifest, through the existing OIDC role and after the version and `main` checks that stand before any credential.

2. **The names are `operator`, `guest` and `control`.** The local tags `make operator-image` and `make guest-image` write change from `gaon-operator` and `gaon-guest` to `operator` and `guest`, so the name built is the name pushed. The `org.opencontainers.image.title` labels are `garam-agent-operator-<image>` for all three.

3. **`make build-images` builds all three**, through `operator-image`, `guest-image` and a new `control-image` it depends on. The guest needs a registry login, so a person without one builds the other two by their own targets.

4. **The credential and the registry login come before the build.** The push role reads the three sources, so the one login serves the guest build's pulls and the pushes, and no other credential and no secret value enters the workflow. The push stays the single irreversible step: every reference is verified and every image tagged before the first push, and a failure in the build or the checks leaves nothing published.

5. **`hack/imageref` knows the three repositories.** Under `garam-agent-operator/` it refuses any repository but `operator`, `guest` and `control`, and then applies ADR 0078's tag rule, unchanged, to each. `make verify-image-ref` is that program.

6. **Nothing re-pushes a tag.** The repositories are immutable, so each reference is pushed once; a second push of a name fails. A run that fails after its first push leaves the version half-published, which only the next version repairs, as ADR 0051 already accepted for two images.

## Consequences

- `v0.1.0-dev.5` is the first release to carry all three. It is tagged by the person who promotes, as ADR 0051 and ADR 0081 say, and reported on `garamsh/aurora#790`.
- A fourth image or a renamed one is a change to `hack/imageref` and to the workflow's list together, so the check and the push cannot drift apart silently. The list is in two places in this repository, the program and the workflow, because the workflow's loop names what it pushes.
- A reference to `garam-agent-operator/gaon-operator` or `gaon-guest` is refused where it is checked. Nothing published ever carried those names.
- A release run now takes the registry credential before the build, so a build failure happens with a credential held. The credential is short-lived, and what it can do is what the push role can do whether or not the build ran first.
- `delivery.md`, `integration.md` and `gaon-operator.md` describe the three images; ADR 0078 and ADR 0051 stand and are not superseded.
