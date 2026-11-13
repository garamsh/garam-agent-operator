# ADR 0028: Run the checks and the e2e suite on the promotion to `main`, and nothing on `dev`

> Status: accepted
> Date: 2026-09-27

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0015](0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md) cut the runs per commit from four to one or two. `Checks` ran on a pull request into `dev` and on a `push` to `dev` and `main`; `E2E Tests` ran on a `push` to `dev` and `main`. So every task still cost three runs: `Checks` on its pull request, and both workflows on its merge.

Measured with `gh run list` on 2026-09-27, for runs created on 2026-09-25 (UTC): five merges into `dev` — `b0974d2`, `03cf874`, `329eb3a`, `6175a02`, `52567d6` — produced fifteen runs, five `Checks` on `pull_request` and five each of `Checks` and `E2E Tests` on `push`. Every one of the fifteen passed.

The account owner's direction, given on 2026-09-06 and restated on 2026-09-27 (issue #175): work lands on `dev` without CI, to save runner cost, and `main` is promoted in batches. It was not applied before this decision.

ADR 0015's promotion is unchanged by that direction. `main` advances only when a person opens a pull request from `dev` to `main` at a published commit and merges it without squashing. That promotion is already the one point where a batch of `dev` is examined as a whole before it becomes released state.

## Decision

**Neither workflow runs for anything that happens on `dev`. Both run on a pull request into `main` and on nothing else. The author's local run of `make ci` and `make test-e2e` is the only gate before `dev`.**

This supersedes ADR 0015's trigger decisions — §`Checks` keeps its pull-request trigger and §`E2E Tests` runs only where a change lands. Its promotion decision, §`main` advances only by a human promotion, at a published commit, stands.

### Triggers

| Workflow | Runs on | Why |
|---|---|---|
| `Checks` | `pull_request` with base `main` | The promotion is where a batch of `dev` is checked before it becomes released state. |
| `E2E Tests` | `pull_request` with base `main` | Same, and it is the only automated run of the layer that exercises the built artifact. |

No `push` trigger remains on either workflow.

- **Not on `push` to `main`.** The promotion merges without squashing, and `main` moves only by a promotion, so the merge commit's tree is the merge result the pull-request run already checked. A `push` trigger would run that tree a second time for one promotion.
- **Not on `push` to `dev`, nor on a pull request into `dev`.** That is the owner's direction, and it is the whole of the saving.
- **A promotion pull request is opened when the promotion is performed, and merged then.** It is not held open while `dev` moves. Its head is `dev`, so while it is open every merge into `dev` is a `synchronize` on it and fires both workflows. That would restore a run per merge through the pull-request trigger. PR #132 was held open that way from 2026-09-05 and was closed on 2026-09-27.

### What gates a merge into `dev`

Before this decision, a pull request into `dev` carried a `Checks` run a reviewer could read. It no longer does. What stands in its place:

- **The author runs `make ci` and `make test-e2e` before pushing.** `ci.md` §Run the checks before pushing already required the first. `README.md` states it where the commands are.
- **The reviewer relies on the pull request's reported run.** The template's Verification field carries the commands the author ran and their results, and a reviewer reads that report rather than a check suite on the head commit.

## Consequences

- **A task costs no runner time.** Applied to 2026-09-25, the fifteen runs become none, and the batch they belong to costs two runs when it is promoted, plus two for each later update to the promotion pull request's head.
- **A break reaches `dev` unobserved.** Under ADR 0015 a merge that broke either suite was reported on `dev` within minutes. Now it is found by the next contributor's local run, or at the promotion. The cost is that every task branched from `dev` after a break inherits it, and the first person to see it may not be its author.
- **The e2e suite is unguarded on `dev`**, as it was on a pull request under ADR 0015. The difference is that it is now unguarded after the merge as well.
- **A reported local run cannot be confirmed by the reviewer.** The pull request's Verification field is the author's statement. Nothing on the forge ties it to the head commit.
- **The promotion carries more.** A failing run on the promotion pull request names a batch, not a task, and finding the commit that broke it is bisection over `dev`.
- **Nothing enforces any of it**, as under ADR 0015. No check is required to merge into `main`, and a promotion pull request whose runs failed can still be merged.

## Rejected alternatives

- **Keep `Checks` on pull requests into `dev` and move only `E2E Tests`.** `Checks` was five of the fifteen runs on 2026-09-25. The owner's direction is that work lands on `dev` without CI, not with less of it.
- **Also run both on `push` to `main`.** That is the same tree the promotion pull request checked, which is the duplication ADR 0015 exists to remove. It would find something new only if `main` had moved by some means other than a promotion, which ADR 0015 forbids.
- **A scheduled run on `dev`.** It restores a signal on `dev` at a fixed cost, but it runs whether or not `dev` moved, and a failure on it names a window of merges no smaller than a promotion does. If a break on `dev` goes unnoticed for long enough to cost more than the runs saved, that is the evidence to reopen this.
- **Gate the promotion's jobs on draft state, so a draft promotion pull request can stay open.** Each merge into `dev` would still create a run on the draft, with its job skipped. That is a workflow run for a push to `dev`, which the owner's direction excludes, and a skipped run is noise on every merge.
