# Integration

How a change reaches `dev` and then `main`, and which checks run at each step.

## Current decisions

- **Two entry points carry every check, and CI invokes them by name.** `make ci` is lint, format, test and build; `make test-e2e` builds a Kind cluster, runs the e2e suite against it and tears it down. `.github/workflows/checks.yml` runs the first and `.github/workflows/test-e2e.yml` runs the second, each as a single `make` invocation. Neither workflow spells out a command inside a target, and neither holds a tool version the `Makefile` does not.
- **Nothing runs on `dev`.** No workflow fires for a push to `dev` or for a pull request into it. Both run on a pull request into `main`, which is the promotion below, and on nothing else.

| Workflow | Runs on | Why there |
|---|---|---|
| `Checks` | `pull_request` with base `main` | The promotion is where a batch of `dev` is checked before it becomes released state. |
| `E2E Tests` | `pull_request` with base `main` | Same, and it is the only automated run of the layer that exercises the built artifact. |

- **No `push` trigger remains.** The promotion merges without squashing and `main` moves only by a promotion, so the merge lands the tree the pull-request run already checked. A run on the `push` to `main` would check that tree twice for one promotion.
- **The author's local run is the only gate before `dev`.** A contributor runs `make ci` and `make test-e2e` before pushing, which `README.md` states where the commands are. A pull request into `dev` carries no check suite, so a reviewer relies on the run the author reports in the pull request's Verification field, and nothing on the forge ties that report to the head commit.
- **A green run means the promotion passed.** It is reported on the promotion pull request, against the batch it carries. A break a merge brings to `dev` is not reported on `dev`: it is found by a later contributor's local run, or at the promotion.
- **Neither workflow's credential can write.** Each sets `permissions: {}` at the top and grants `contents: read` on its one job. A step that needs a write grant takes it on the job that needs it and nowhere higher.
- **`main` advances only by a human promotion, at a commit that has been published.** Opening a pull request from `dev` to `main` at that commit is a step in the publish sequence `delivery.md` holds, and it is merged without squashing: a squash mints a new commit, and an image tag is the abbreviated hash of the commit built, so every published tag would stop resolving on the branch holding released state.
- **A promotion pull request is opened when the promotion is performed, and merged then.** It is not held open while `dev` moves: its head is `dev`, so every merge into `dev` would update it and fire both workflows, one run per merge again.
- **The GitHub default branch is `dev`.** That is a repository setting rather than anything in these files, so what follows is what this repository requires of it, not a description of what it holds. Two things rest on it: a pull request opened from the forge targets `dev` without the author choosing, which is what `git.md` §Branches requires of every task; and GitHub reads `.github/dependabot.yml` from it, so the pin-bumping bot that `ci.md` §Verify a pinned dependency requires a pinned dependency to name runs only once the setting is `dev`.
- **Nothing here enforces any of it.** No branch is protected, no check is required to merge, and neither workflow is named as a gate. The triggers decide what runs, not what must pass.

## Rationale

Which events fire each workflow is [ADR 0028](adr/0028-run-the-checks-and-the-e2e-suite-on-the-promotion-to-main-and-nothing-on-dev.md). It applies the owner's direction that work lands on `dev` without CI and `main` is promoted in batches (issue #175), after five merges into `dev` on 2026-09-25 produced fifteen runs. Leaving the gate before `dev` to the author's local run rests on #108 and #113, which together made `make test-e2e` a command a contributor can run against a cluster the run owns.

What advances `main` is [ADR 0015](adr/0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md), settled on issue #115. Its trigger decisions are superseded by ADR 0028; its promotion decision stands.

That `main` is released state and `dev` is where work integrates is [ADR 0002](adr/0002-dev-integration-branch.md), which named the promotion as an explicit act nothing described. ADR 0015 describes it. That the promotion cannot squash follows from [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md), which fixes an image tag as a commit's abbreviated hash so a reader of a deployed reference can run `git show` on it.

## Open questions

- **`main` holds none of the released state it is for.** Measured on 2026-09-01: `origin/main` was `60c6f84 "Initalize project"`, 58 commits behind `origin/dev`, and `git branch --contains` put none of the three published commits — `7792ebe`, `f02d007`, `da07a03` — on it. Writing the promotion down does not perform it, and the repair is an act on the repository rather than a change to it.
- **Whether a break on `dev` goes unnoticed for too long.** No run observes `dev`, so a merge that breaks either suite reaches the branch every task branches from and is found by whoever runs the suite next. Nothing has yet measured how long that takes here or what it costs; a break found late enough to cost more than the runs saved is the evidence that reopens ADR 0028.
- **Whether `main` should be protected.** Never pushing to `main` is held by discipline: `git.md` §Branches states it, and `gh api .../branches/main/protection` returned 404 on 2026-09-01. It becomes load-bearing once `dev` is the default and `main` means released state. Issue #115 recorded it rather than changing it, because requiring review blocks every merge while one account holds both roles.
