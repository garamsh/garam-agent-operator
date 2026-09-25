# ADR 0011: The conventions template is the frame, and a divergence is earned by a fact

> Status: accepted
> Date: 2026-08-27

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

`docs/convention/` came from `garamsh/convention-driven-project`, and a `template` remote points at it. Nothing recorded what that relationship is. The remote and the `sync-conventions` skill together describe a *procedure* — fetch, compare, adopt — and say nothing about who wins when the template's text and a local preference disagree.

The question is not theoretical and it was answered under pressure. Three syncs landed on 2026-08-23 and 2026-08-24 (#59, #68, #70), and the answer decided each of them:

- The ADR errata exception was recommended here for **rejection**, on the ground that this repository had met no instance of the case it covers. Taken instead, it forced the append-only notice in six merged ADRs to be corrected — a consequence the template cannot produce, because it holds one ADR file and that file is not an ADR.
- `documentation.md`'s scope was widened from `docs/` to every Markdown document, which surfaced the root `README.md` violating the Contents rule. A project selecting from a menu would have declined the widening and kept the violation.
- `docs/convention/documentation.md:36` was **shipped knowingly stale**, because the repair belonged upstream and a local variant would have to be reconciled with theirs later. It was fixed upstream two days on and adopted with no reconciliation.

So the decision has already produced outcomes in both directions: it took a rule this project had argued against, and it declined a fix this project could have written.

What still has no record is the boundary. Four files diverge from the template today, derived rather than declared — for each shared path, the newest template commit whose blob equals ours; no match means the file was adapted here:

```
.github/CODEOWNERS
docs/architecture/README.md
docs/convention/README.md
docs/convention/git.md
```

Each is individually defensible and collectively unexplained. Unrecorded, every sync re-argues which of them are still earned, and the adapted region is exactly where a missed update hides — a locally adapted line and a stale line are indistinguishable to that derivation, which reports the file and stops.

## Decision

**The template is the frame. Its text is taken as written, and a divergence is what needs a reason.**

Not the reverse. The project does not select from the template as a menu, and "we prefer different words" is not a reason. Where a rule the template states would bind this project and this project would rather it did not, the answer is a proposal upstream, not a local edit.

**A divergence is earned when the template's text cannot be true here — never when local text would be better.**

That is the whole test, and it is a test rather than a list because a list goes stale on the next sync. Two ways a template sentence fails to be true of this project:

- **It describes a shape this project does not have**, because a decision recorded here settled that shape differently.
- **Its content is an inventory of what a project holds** — an owner, an index, a table of the files this project keeps. The template ships a form; the entries are the project's by construction.

**A defect in the template is not a divergence.** Where the template's text is wrong, the repair is reported upstream and the defect is carried here until it lands, recorded in the pull request as a known and accepted cost. A local fix trades a defect that will be repaired for a variant that must be reconciled forever, and this project has already paid that trade once in the right direction.

**The four current divergences are each tested against that, and each holds:**

| File | Why the template's text cannot be true here |
|---|---|
| `docs/convention/git.md` | Branch from `main`, merge to `main`. [ADR 0002](0002-dev-integration-branch.md) settled that this project integrates on `dev`, so the template's text describes a shape this project does not have. |
| `.github/CODEOWNERS` | Ships `@project-owner-placeholder`, which the template's own bootstrap step says to replace. It asks to be adapted. |
| `docs/convention/README.md` | Its tables are the inventory of the convention files this project keeps, and its §Architecture section records that this project has none. The form is the template's; the entries cannot be. |
| `docs/architecture/README.md` | Its index lists this project's responsibility documents and ADRs. Same reason. |

**Nothing else diverges, and the set is derived rather than declared.** Walking `git rev-list template/main -- <path>` newest-first and stopping at the first commit whose blob equals ours answers, per file, whether it is a stale copy of a known revision or a local adaptation. "No ancestor blob matches" *is* what an adaptation looks like, so a file leaves the set the moment it stops being adapted, and no list has to be maintained.

## Consequences

Easier: a sync is a mechanical act with one question in it — does this file's text stop being true here — rather than a negotiation over each hunk. The three syncs that preceded this decision each took under an hour, and two of them adopted rules this project had reasoned against.

Harder, and this is what the decision gives up: **this project cannot fix a convention it believes is wrong.** It can propose, and it must wait. `documentation.md:36` was carried stale for two days for exactly that reason, with the defect recorded in the pull request rather than repaired. That cost is the point rather than a side effect — the alternative is a variant nobody can reconcile.

**The derivation has a blind spot the decision does not close.** It classifies by file, so an adapted file's *other* lines are invisible to it. Upstream commit `#238` changed two lines in `git.md`; the first pass here took one and left the other, because the untaken line already differed for the `dev` model and read as adaptation rather than staleness. The remedy is to read the upstream *commits* for an adapted path — `git log -p <syncpoint>..template/main -- <path>` — rather than the file's diff, and it is a habit rather than a mechanism.

Ruled out: **declaring the divergence set in a file.** A list is a second place to keep correct, and this project has spent the week cataloguing what a stale record costs; the derivation reads the only two things that are true now, the template's history and this tree's blobs. Ruled out with it: **a recorded marker of the last synced commit**, which was offered by the template's own maintainer and withdrawn after the only project running one was found asserting a state that never existed.

Not decided here: whether any convention this project currently keeps should be proposed for change upstream. That is a per-rule question and this decides only where such a question goes.

**No responsibility document is affected.** `docs/architecture/README.md` Rule 3 pairs an ADR with the documents describing what it changed, and this changes no domain of the system — it governs how a rule enters the repository. `docs/architecture/agent.md` describes the `Agent` API and its controller and has nothing to say about it. [ADR 0002](0002-dev-integration-branch.md) is the precedent: a process decision, recorded with no responsibility document beside it, because there was none to pair with.

## Errata

### 2026-08-31 — §Architecture was an unadopted deletion, not an adaptation

Decision's table gives two grounds for `docs/convention/README.md` diverging: "Its tables are the inventory of the convention files this project keeps, and its §Architecture section records that this project has none." The first holds. The second is wrong, and was already wrong on the date above this entry's own.

§Architecture was not a section this project wrote and kept. The template carried one and deleted it in `f5706ae` (`convention-driven-project#294`), which in the same commit removed `docs/convention/arch-domain.md`, the section's §Contents entry, precedence tier 2, and the plural in "bootstrap prunes only the tables below". `git merge-base --is-ancestor f5706ae d796d58` holds, and `d796d58` is the template commit PR #70 synced to, so that deletion stood in front of this project and was not taken. PR #68 read the same commit and recorded two of its effects — the deleted file and the tier — as nothing to do, both correctly, and named neither the section nor the plural.

So the clause cited here as evidence of an earned adaptation was evidence of the opposite. An absent section has no text that could fail to be true here, and "the template's text cannot be true here" is the test this Decision states, so the section could never have passed it. It was an unadopted deletion carrying an adaptation's reason.

The decision stands, and so does the row. Its first ground carries it alone: §Stack-specific's table lists the three stack files this project keeps, which is an inventory the template ships no entries for. `docs/convention/README.md` remains adapted with §Architecture gone.

This is also the third instance of the blind spot Consequences names, after upstream `#238` and the §Rules case closed by PR #100 — and the first where the missed lines sat in a file the derivation had already classified as adapted for a reason that was itself false.

Falsified on issue #69.
