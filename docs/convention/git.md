# Git Conventions

How branches, commits, and PR titles are written, and what survives a merge.

## Branches

- `feat/<short-name>` — new behavior
- `fix/<short-name>` — bug fixes
- `chore/<short-name>` — tooling, config, dependencies
- `docs/<short-name>` — documentation only
- `refactor/<short-name>` — behavior-preserving restructure
- `test/<short-name>` — test-only changes

One task, one branch. Branch from `dev`; never commit to `dev` or `main` directly.

`dev` is the integration branch: it is the base every task branch starts from and the target every pull request is opened against. `main` advances only from `dev`, by a release, never by a task.

## Commits

Only the PR title reaches `dev`, together with the trailer naming the squashed commits' authors. A branch's commit messages are otherwise a working record for the reviewer reading the branch, not the permanent history.

Format: `<type>: <imperative summary>`

- Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`
- Summary: imperative mood, lowercase, no trailing period. `fix: reject empty tokens`, not `fixed some bugs`.
- One concern per commit. If a change needs "and" to describe it, split it.
- Omit the body unless the reason for the change is invisible in the diff. A body never restates what changed.
- The message contains only what the project put there — no line a tool appended unasked. A `Co-authored-by:` naming an author of the squashed commits is not such a line: squash drops those authors and the trailer is where they survive.
- Reversing an earlier decision mid-branch is an ordinary commit describing the new state. Earlier commits are not rewritten to hide that the decision changed.
- A point about a commit message is fixed in the commit that carries it — amend or rebase, then force-push the same branch. A later commit cannot remove text from an earlier one, so it cannot carry that fix. This is the one case where rewriting a branch commit is right.

## PRs

- Title follows the commit format: `<type>: <imperative summary>`. It becomes the commit on `dev`, so it is the permanent record of the change.
- Body follows `.github/PULL_REQUEST_TEMPLATE.md`.
- PRs are squash-merged. Branch commits do not appear on `dev`; do not rewrite them to be pretty.
