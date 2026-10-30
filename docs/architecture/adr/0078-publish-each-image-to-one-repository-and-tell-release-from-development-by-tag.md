# ADR 0078: Publish each image to one repository, and tell release from development by tag

> Status: accepted
> Date: 2026-10-30

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #401. Three decisions of 2026-10-28 reach this project:

- **The owner's naming rule.** An ECR repository is named `<publishing repository>/<image>`, and the image name never repeats the prefix (`garamsh/aurora#709` §Context).
- **The owner's decision on existing repositories**, relayed by garam's PM and confirmed by the user in this project's PM session. Existing repositories move to that rule, there is no separate development path, `garam-dev/` goes away, and release and development images share one repository, told apart by tag. Migration copies each tag by digest, so pins do not change, and retires the old repository with the owner's approval.
- **The user's image names**, `manager` and `control`, in the same session.

What that replaces:

- **[ADR 0066](0066-push-development-images-to-garam-dev-and-keep-garam-for-releases.md)** split each image into a repository under `garam/` for releases and one under `garam-dev/` for development.
- **[ADR 0067](0067-tag-a-development-image-with-its-commit-hash-alone-and-refuse-a-push-the-namespaces-forbid.md)** made `hack/imageref` refuse what those two namespaces forbid. Its table is written in namespaces, so it cannot outlive them.
- **[ADR 0033](0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md) and [ADR 0051](0051-publish-a-release-from-a-version-tag-on-main-through-a-release-workflow.md)** name the release repositories.

`garamsh/aurora#711` creates the repositories, copies the tags and retires the old repositories. This project's names and tag forms are recorded on it, in the comment of 2026-10-28T11:55:05Z. It was open on 2026-10-30.

## Decision

1. **Each image has one repository, holding its release and development images.**

   | Image | Repository |
   |---|---|
   | Manager | `garam-agent-operator/manager` |
   | Control service | `garam-agent-operator/control` |

   They are in account `486152169996`, region `ap-northeast-2`. [ADR 0014](0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md) applies to each: `IMMUTABLE`, and a deployment references a digest with the tag beside it. The two images keep separate repositories because both built from one commit claim the same commit tag, as ADR 0033 records.

2. **The tag says which role.** A release is `X.Y.Z`, as ADR 0051 decides. A development image is the commit's 12-hex hash alone, as ADR 0067 decides. A release tag carries dots and a hash tag carries none, so the two forms cannot collide and neither needs a mutability exclusion.

3. **`hack/imageref` decides by the tag, within `garam-agent-operator/`.** Every path that pushes runs it, as ADR 0067 set out, and a release is recognized as ADR 0067 set out.

   | Reference | Verdict |
   |---|---|
   | Under `garam-agent-operator/`, tagged `X.Y.Z`, in `release.yml`'s run for the tag `vX.Y.Z` | accepted |
   | Under `garam-agent-operator/`, tagged `X.Y.Z`, anywhere else | refused |
   | Under `garam-agent-operator/`, tagged with exactly 12 lower-case hex digits | accepted |
   | Under `garam-agent-operator/`, any other tag or no tag, a `dev-` tag included | refused |
   | Outside `garam-agent-operator/` | no rule applies |

   - **`GITHUB_WORKFLOW_REF` narrows a release tag and nothing else.** A hash tag is accepted in any run, because the check answers what a tag may be, and `release.yml` pushes only its own version.
   - **The image name is not checked.** Any repository under the prefix follows the rule. A repository that does not exist refuses the push on its own, and a list here would be a second copy of the table above.
   - **The limit is ADR 0067's.** The check stops mistakes, and IAM stops the rest.

4. **A development image is pushed by a person, under their own identity, into `garam-agent-operator/<image>`.** The push role's subject stays `refs/tags/v*`, so no development push can use it. The hand procedure in `delivery.md` keeps its steps and takes the new reference.

5. **`make build-images` tags its output with the repository's name**, `garam-agent-operator/manager:<commit>` and `garam-agent-operator/control:<commit>`. `release.yml` then loops over `manager` and `control`, with no table from a local name to a repository.

This supersedes:

- **ADR 0066**, in full.
- **ADR 0067's refusal by namespace**, its table and its "Under neither" case. Its hash tag alone, where the check runs, how a release is recognized, how a reference is read, and its limit stand.
- **The repository names in ADR 0033 and ADR 0051.** ADR 0033's two images, entry points and Dockerfiles stand, and so does ADR 0051's release workflow, tags and push role. ADR 0033's one repository per image, which ADR 0066 superseded for development builds, is restored.

[ADR 0026](0026-single-ecr-repository-dev-tag-by-role.md) stays superseded. It shared one repository too, but marked the role with a `dev-` tag, which ADR 0067 removed and this decision does not bring back.

## Consequences

- **Repositories come first.** `release.yml` names `garam-agent-operator/<image>` from this change on, so a `v*` tag pushed before both repositories exist, and before the push role's `repository_names` reach them, fails. No release is tagged until then. Merging this change does not depend on it: the issue's comment of 2026-10-28T11:55:12Z says so.
- **The hand procedure depends on `garamsh/aurora#711`.** `make verify-image-ref` runs on a new reference today. Pushing and reading back need the repositories and a person-identity write into them. Until then the procedure cannot be completed.
- **`release.yml` is unexercised against these repositories.** Its first run is the next release after they exist.
- **A deployed pin changes path and nothing else.** Each tag is copied by digest, so a reference keeps its tag and digest. Repointing the references and retiring the old repositories are `garamsh/aurora#711`'s.
- **The check no longer refuses a stale reference.** ADR 0067's consequence, that a reference naming `garam/` fails before anything is built, is gone: the old repositories fall outside the prefix, so the check says no rule applies. Until they are retired, IAM alone stops a push there. Keeping them in the check would keep dead names in the code to guard repositories that are leaving.
- **Which tags expire is `garamsh/aurora#711`'s.** Whatever selects development tags by pattern has to match the 12-hex form and never `X.Y.Z`. A release tag never expires and never moves.
- **Garam's test image is not moved here.** `GARAM_TEST_REPOSITORY` in the `Makefile` is garam's image and moves when garam moves it.

## Rejected alternatives

- **Keep `garam-dev/`, or a separate development repository under the new prefix.** The owner's decision refuses a separate development path.
- **Bring back a `dev-` tag to mark the role in a shared repository.** The forms already differ, ADR 0067's reasons stand, and an immutable repository never gives a tag back.
- **Repeat the prefix in the image name, as `garam-agent-operator/garam-agent-operator`.** The owner's rule refuses it.
- **Refuse a hash tag inside a release run.** It would guard against one line of `release.yml` from a check that the same line calls, and it would add a condition to the half of the rule that needs none.
- **Keep a local name per image and map it to a repository in `release.yml`.** That is a table a rename can leave behind. One name, from the build to the registry, leaves nothing to map.
