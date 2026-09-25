# ADR 0015: Run the e2e suite where a change lands and not on every pull request, and advance `main` only by a human promotion

> Status: accepted
> Date: 2026-09-01

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Measured on 2026-09-01. `.github/workflows/checks.yml` and `.github/workflows/test-e2e.yml` both carried `on: push:` and `on: pull_request:` unqualified, so every commit on an open pull request produced four runs. On PR #114's head `32f5e57a`, `gh run list --commit` returned `Checks` and `E2E Tests` once each for `push` and once each for `pull_request` — four runs, one commit. Across 2026-08-31 the repository created 38 runs, 19 of each workflow, from 5 merges into `dev` and 6 pull-request head updates. `E2E Tests` takes roughly three minutes, so that day spent about 57 minutes of runner time on the e2e layer alone. The account owner has said the usage is not affordable, and unlike the sibling repositories this project's Actions actually run.

The `push` and `pull_request` runs of a workflow on a pull-request commit are not two signals. The first runs the branch tip and the second runs that tip merged into the base, and on a branch cut from a `dev` that has not moved those are the same tree. The one visible divergence on `32f5e57a` — the `push` run of `Checks` failed while the `pull_request` run passed — was `sum.golang.org` returning `INTERNAL_ERROR` mid-verification, a transport failure and not a difference in what was checked.

The two workflows are not alike, and the sequencing that separates them closed this week.

- **`Checks` is `make ci`** — lint, format, test, build, and a working-tree-clean assertion. It needs no cluster, and a contributor reproduces it exactly with one command.
- **`E2E Tests` is `make test-e2e`**, which builds a Kind cluster and is the only layer that exercises the built artifact. Until #108 a contributor could not run it at all: `kind` was neither pinned nor installed. Until #113 running it could act on whatever cluster was ambient. Both landed on 2026-08-31, so the precondition for taking a check off every pull request — that its author can run it, on a cluster the run owns — holds for this suite for the first time.

`main` is the second half of the same question, and its state is measured rather than argued. `origin/main` is `60c6f84 "Initalize project"`, 58 commits behind `origin/dev`, and holds neither `.github/workflows/` nor `.github/dependabot.yml`. So nothing runs on `main` today, and Dependabot — which reads its configuration from the default branch — has never read the file this repository wrote for it. `docs/convention/git.md` §Branches already states the model the repository does not implement: `dev` is the integration branch and `main` advances only from `dev`, by a release. The GitHub default branch is `main`.

[ADR 0002](0002-dev-integration-branch.md) named this gap when it made `dev` the integration branch: "Promotion from `dev` to `main` is now an explicit act that nothing in the conventions yet describes; the first release will have to settle how it happens." Nothing described it, and in 58 merges it was never performed. Three images have been published in that time, tagged `7792ebe`, `f02d007` and `da07a03` ([ADR 0014](0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md)), and `git branch --contains` puts none of the three on `main`. Released state exists and `main` does not hold it.

## Decision

**Each check runs once per commit, on the branch a change lands on and, for everything but the e2e suite, on the pull request as well. `main` advances only when a person promotes `dev` to it at a commit they have published.**

### `Checks` keeps its pull-request trigger

It is the cheapest guard the project has and it is the only automated one a pull request gets. It also runs on `push` to `dev` and `main`, because a squash merge produces a commit no pull-request run saw: the merge result is tested before `dev` moves, and what lands is a different commit if it moved in between. `dev` is the branch every task branches from, so a break there is a break for every worker after it.

The pull-request trigger is filtered to `branches: [dev]`. That is what makes the duplication go rather than move: the promotion pull request below has `dev` as its head, so an unfiltered trigger would run the same commit twice again — once for the `push` to `dev` and once for the pull request opened from it — and its merge into an ancestor resolves to `dev`'s own tip, which the `push` run already ran.

### `E2E Tests` runs only where a change lands

No pull-request trigger. It runs on `push` to `dev` and to `main`.

The rule this follows is that CI runs what nobody runs by hand. A merge is that: it is a commit no author produced and no author can run before it exists. A pull-request head is not — since #108 and #113, `make test-e2e` builds and tears down a cluster the run owns, on any contributor's machine, which is the same suite by the same name that CI would invoke. Spending three minutes of the project's runner time on every head update to repeat what the author is already required to run buys the project a second opinion about the author's diligence, at the cost the owner has said the project cannot carry.

What this gives up is stated rather than minimised: a pull request no longer carries evidence that the e2e layer passes, and the first automated e2e run against a change happens after it is merged. The break is caught on `dev`, which is the branch that exists to absorb it, and one merge before `main`.

### `main` advances only by a human promotion, at a published commit

The trigger is publishing. `delivery.md` already describes a sequence a person follows to publish an image — authenticate, check out the commit, confirm the tree is unmodified, build, push, hand the deployment the tag and digest — and the promotion is a step in that sequence: open a pull request from `dev` to `main` at the published commit and merge it without squashing.

Not squashing is required by what a tag means. `delivery.md` fixes an image tag as the abbreviated hash of the commit built, so that a reader holding a deployed reference can run `git show` on it. A squash of `dev` into `main` mints a new commit and every published hash stops resolving on the branch that is supposed to hold released state.

`git.md` §PRs says pull requests are squash-merged, and its subject is the task pull request: the surrounding clauses are that the title "becomes the commit on `dev`" and that "branch commits do not appear on `dev`". A promotion pull request lands on `main`, not `dev`, and the commits it carries are already one per task, having been squashed on the way into `dev`. Squashing again would destroy the property the first squash was for. This is settled the way the nearest convention settles its own and is raised for the PM in the pull request that records this decision.

## Consequences

- **A worker pull request costs one run.** Applied to 2026-08-31 as measured, the day's 38 runs become 16: 6 pull-request head updates at one `Checks` run each, and 5 merges into `dev` at two runs each. The e2e layer drops from 19 runs to 5, from about 57 minutes of runner time to about 15.
- **A green pull request is a weaker signal than it was**, and a contributor has to know it. It says `make ci` passed on the merge result; it says nothing about the e2e layer. `README.md` carries that where a contributor meets the commands.
- **Running `make test-e2e` before pushing stops being optional in practice.** `ci.md` §Run the checks before pushing already required running the project's checks locally. Nothing here weakens a target: `make ci` and `make test-e2e` run exactly what they ran, and this changes only which events invoke them.
- **The e2e suite is unguarded between a pull request and its merge.** A change that passes `make ci` and breaks the cluster reaches `dev` and is caught by `dev`'s own run. That is the cost, and it is bounded by `dev` being the branch that absorbs it.
- **`main` is behind by definition whenever commits are unpublished**, which is the correct state rather than drift. Today it is behind by 58 commits with three commits published, which this decision names as wrong and does not repair: the repair is a promotion, an act on the repository rather than a change to it.
- **Nothing enforces the promotion.** It binds whoever publishes. `main` is not a protected branch, no required check exists on either branch, and this adds neither.
- **Making `dev` the GitHub default branch is a repository setting this change does not make.** It is what puts a pull request's base at `dev` by default and what makes GitHub read `.github/dependabot.yml`, which has never been read from `main`. Every file in this repository is prepared for it.

## Rejected alternatives

- **One rule for both workflows.** It is the tidy answer and it is wrong in either direction: applied as "pull request only" it leaves `dev` and `main` unchecked, and applied as "push only" it removes the merge-result check that a pull request is the only place to get. The two workflows differ in what a run costs and in whether an author can reproduce it, which is exactly what a trigger should be chosen on.
- **Keeping `E2E Tests` on pull requests behind a `paths` filter.** The suite exercises the built artifact, so nearly any change under `api/`, `internal/` or `cmd/` is relevant and the filter would fire on most pull requests for most of the cost. It also adds a second list to keep in sync with what the suite covers, and a filtered workflow reports no run rather than a passing one, which reads as a missing check to anyone who later requires it.
- **Automatic promotion — a job that advances `main` on every green `dev`.** Refused on three grounds. It makes `main` mean "the latest green `dev`", which is a delayed copy of `dev` and not the released state ADR 0002 chose it for; a branch that always equals another carries no information. It reintroduces per-merge runs — a push to `main` fires both workflows again, so the four-runs-per-commit shape reappears one branch over, against the cost this decision exists to cut. And it needs `contents: write` on an unprotected branch, the only write grant in a repository that has none, manufactured by the choice rather than required by a job.
- **Manual promotion on a schedule, or at a cadence.** A cadence advances `main` past commits nobody published, which is the same defect as automatic promotion with worse timing.
- **Treating the 58-commit gap as evidence against manual promotion.** It is evidence against an unstated trigger, which is what ADR 0002 recorded as an open consequence and what this decision closes. What would change the decision is the gap recurring after the trigger is written down and placed in the publish sequence: a promotion that is a named step and is still skipped is a mechanism failing, and automation would then be earned on evidence rather than on prediction.
- **Requiring the checks on `dev` or protecting `main`.** Both are repository settings, both are outside a change to the repository's files, and the template's own `README.md` records why review requirements are absent where one account holds both roles. Issue #115 excludes protection deliberately.
