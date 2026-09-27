# ADR 0002: Integrate on `dev` and keep `main` for released state

> Status: accepted
> Date: 2026-08-16

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The conventions this project adopted branch every task from `main` and merge every pull request back into it, which makes `main` the integration point. The project owner asked instead that `main` be protected from day-to-day work and that task branches and pull requests both use a `dev` branch.

Both arrangements protect `main` from direct commits. They differ in what `main` means: the tip of continuous integration, or a branch that only ever receives reviewed, integrated work.

## Decision

`dev` is the integration branch. Task branches start from `dev`, pull requests target `dev`, and `main` advances only from `dev`.

## Consequences

Easier: `main` can carry a stricter protection rule than the branch that receives every merge, and its history reads as released state rather than as a log of individual tasks.

Harder: there is a second long-lived branch to keep current, and a task branch left open drifts from `dev` rather than from `main`. Promotion from `dev` to `main` is now an explicit act that nothing in the conventions yet describes; the first release will have to settle how it happens.

Ruled out: branching a task directly from `main`, and opening a pull request against `main` for ordinary work. `docs/convention/git.md` §Branches, §Commits, and §PRs were changed to state `dev` in the pull request that recorded this decision.
