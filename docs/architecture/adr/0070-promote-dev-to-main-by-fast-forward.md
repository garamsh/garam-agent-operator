# ADR 0070: Promote `dev` to `main` by fast-forward

> Status: accepted
> Date: 2026-10-09

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0015](0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md) has a promotion pull request from `dev` to `main` "merge it without squashing". A squash mints a commit `dev` never held, so the commit hashes that image tags name would stop resolving on `main`.

A merge commit has the same defect one commit over. The three promotions so far, #191, #198 and #269, were merged that way, as `58dc6d3`, `0165c66` and `4023d7e`. `dev` never held any of them, so `main` and `dev` have diverged: on 2026-10-09, `git rev-list --left-right --count origin/main...origin/dev` answered `3 42`. The two projects that fast-forward, sherlock and garam, do not have this problem (#335).

`git.md` §PRs, synced from the template in #336, now states the rule: "A promotion is a PR from the default branch into a branch that only promotions move. It is merged by fast-forward, never by a squash, a rebase or a merge commit: each of those leaves that branch holding a commit the default branch never held."

Nothing depends on `main`'s three merge commits. Each of the following was measured on 2026-10-09:

- **Tags.** `git ls-remote --tags origin` returns none, so no release tag names `4023d7e`, `0165c66` or `58dc6d3`.
- **Images.** `aws ecr describe-images` lists these tags:
  - `garam-dev/garam-agent-operator`: `4c425a7e75d9`, `6175a026bfc8`, `6a29c92c9c07`, `7c216469476d`, `7e7f89c84bf7`;
  - `garam-dev/garam-agent-operator-control`: `4c425a7e75d9`, `7c216469476d`;
  - `garam/garam-agent-operator` and `garam/garam-agent-operator-control`: none.

  None names a merge commit on `main`. Image tags name `dev` commits ([ADR 0067](0067-tag-a-development-image-with-its-commit-hash-alone-and-refuse-a-push-the-namespaces-forbid.md)).
- **Content.** `main`'s tip `4023d7edd20cca97776bbdb16b2edb953860470f` has the tree `51b1283ee46c8d3a8f4fda8a07bc32792fed94b5`. So does `dev`'s `7c216469476d1ac23cdbd7d7d75193c057090fa0`, which `git merge-base --is-ancestor` places on `dev`.

## Decision

- **A promotion pull request from `dev` to `main` is merged by fast-forward,** never by a squash, a rebase or a merge commit. Each of those leaves `main` holding a commit `dev` never held. This supersedes ADR 0015's "merge it without squashing". The rest of ADR 0015's promotion stands: `main` advances only by a human promotion, a pull request from `dev`.
- **A fast-forward leaves `main` at a commit `dev` holds.** Every image tag and release tag then names a commit on both branches, and `git merge-base --is-ancestor origin/main origin/dev` succeeds after every promotion.
- **`main` is reset once, to `7c216469476d`, at the next promotion, and then fast-forwarded to `dev`'s tip.** The PM performs the reset, not a pull request.
  - It drops `4023d7e`, `0165c66` and `58dc6d3` from `main` and no content, since `7c21646` holds `4023d7e`'s exact tree.
  - The old tip, `4023d7edd20cca97776bbdb16b2edb953860470f`, is recorded here and on #335, so the reset can be undone.

## Consequences

- **The promotion's title becomes no commit.** The commits it carries keep the titles and trailers their own squash merges gave them on `dev` (`git.md` §PRs).
- **The checks still run.** The workflows trigger on the promotion pull request, which a fast-forward merge still has. What lands on `main` is the head the pull-request run checked, so [ADR 0028](0028-run-the-checks-and-the-e2e-suite-on-the-promotion-to-main-and-nothing-on-dev.md)'s reason for no `push` trigger holds unchanged.
- **The reset rewrites `main` once.** Anyone holding a clone with the old `main` sees a forced update. No tag, image or deployment named the dropped commits.
- **`release.yml`'s check that a tag's commit is on `main` still holds.** After a fast-forward, `main`'s tip is a `dev` commit, and that is what gets tagged.

## Rejected alternatives

- **Keep merge commits.** Each promotion adds a commit `dev` never held, and `main` stays diverged from `dev`.
- **Merge `main` back into `dev` after each promotion.** That puts the merge commits on `dev` and does not stop them being minted. It also adds a commit to `dev` that no task made.
- **Fast-forward without the reset.** That is impossible while `main` holds commits `dev` lacks.
