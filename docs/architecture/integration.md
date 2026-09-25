# Integration

How a change reaches `dev` and then `main`, and which checks run at each step.

## Current decisions

- **Two entry points carry every check, and CI invokes them by name.** `make ci` is lint, format, test and build; `make test-e2e` builds a Kind cluster, runs the e2e suite against it and tears it down. `.github/workflows/checks.yml` runs the first and `.github/workflows/test-e2e.yml` runs the second, each as a single `make` invocation. Neither workflow spells out a command inside a target, and neither holds a tool version the `Makefile` does not.
- **Each check runs once per commit.** A commit that produced a `push` run and a `pull_request` run of the same workflow was being checked twice with one result, and the triggers are written so that cannot happen.

| Workflow | Runs on | Why there |
|---|---|---|
| `Checks` | `pull_request` with base `dev`; `push` to `dev` and `main` | It needs no cluster and it is the only automated check a pull request gets. The `push` triggers cover the squash merge, which is a commit no pull-request run saw. |
| `E2E Tests` | `push` to `dev` and `main` | It builds a cluster and an author can run the same suite by the same name locally, so CI runs it on the merge nobody runs by hand. |

- **The pull-request trigger names its base.** It fires for a pull request into `dev` and not for one into `main`, because the promotion below has `dev` as its head: an unfiltered trigger would run `dev`'s tip a second time, and that tip's merge into an ancestor is the tip itself.
- **A green pull request and a green branch mean different things.** On a pull request it means `make ci` passed on the merge result and says nothing about the e2e layer. On `dev` and on `main` it means both passed on what actually landed. A contributor closes the difference by running `make test-e2e` before pushing, which `README.md` states where the commands are.
- **Neither workflow's credential can write.** Each sets `permissions: {}` at the top and grants `contents: read` on its one job. A step that needs a write grant takes it on the job that needs it and nowhere higher.
- **`main` advances only by a human promotion, at a commit that has been published.** Opening a pull request from `dev` to `main` at that commit is a step in the publish sequence `delivery.md` holds, and it is merged without squashing: a squash mints a new commit, and an image tag is the abbreviated hash of the commit built, so every published tag would stop resolving on the branch holding released state.
- **The GitHub default branch is `dev`.** That is a repository setting rather than anything in these files, so what follows is what this repository requires of it, not a description of what it holds. Two things rest on it: a pull request opened from the forge targets `dev` without the author choosing, which is what `git.md` §Branches requires of every task; and GitHub reads `.github/dependabot.yml` from it, so the pin-bumping bot that `ci.md` §Verify a pinned dependency requires a pinned dependency to name runs only once the setting is `dev`.
- **Nothing here enforces any of it.** No branch is protected, no check is required to merge, and neither workflow is named as a gate. The triggers decide what runs, not what must pass.

## Rationale

Which events fire each workflow, and what advances `main`, is [ADR 0015](adr/0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md). It was settled on issue #115 after the shape was measured: four runs on one commit, 38 runs and about 57 minutes of e2e runner time in a day, against an owner who has said the usage is not affordable. Taking the e2e suite off pull requests rests on #108 and #113, which together made `make test-e2e` a command a contributor can run against a cluster the run owns; before them it was not one.

That `main` is released state and `dev` is where work integrates is [ADR 0002](adr/0002-dev-integration-branch.md), which named the promotion as an explicit act nothing described. ADR 0015 describes it. That the promotion cannot squash follows from [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md), which fixes an image tag as a commit's abbreviated hash so a reader of a deployed reference can run `git show` on it.

## Open questions

- **`main` holds none of the released state it is for.** Measured on 2026-09-01: `origin/main` was `60c6f84 "Initalize project"`, 58 commits behind `origin/dev`, and `git branch --contains` put none of the three published commits — `7792ebe`, `f02d007`, `da07a03` — on it. Writing the promotion down does not perform it, and the repair is an act on the repository rather than a change to it.
- **Whether a merge into `dev` should be gated rather than observed.** The e2e suite runs after a merge lands, so a break reaches the branch every task branches from and is reported rather than prevented. A merge queue is the shape that would run it before the merge, and it is undecided because nothing has measured what queuing would cost against what a post-merge break costs here.
- **Whether `main` should be protected.** Never pushing to `main` is held by discipline: `git.md` §Branches states it, and `gh api .../branches/main/protection` returned 404 on 2026-09-01. It becomes load-bearing once `dev` is the default and `main` means released state. Issue #115 recorded it rather than changing it, because requiring review blocks every merge while one account holds both roles.
