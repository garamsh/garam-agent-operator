# ADR 0004: Extend `stack-go.md` instead of replacing it

> Status: accepted
> Date: 2026-08-17

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

Supersedes ADR 0003.

## Context

ADR 0003 deleted `stack-go.md` and gave `stack-kubebuilder.md` every Go rule, general ones included. It named the cost in its own §Consequences: "the general Go rules now live in two places across the organization — this file and the template's `stack-go.md` — and an improvement to one does not reach the other."

That cost came due. The template rewrote `stack-go.md` §4 Error handling (`2f5871e`), a substantial improvement that could not arrive by sync. Porting it by hand took a working session and turned up a factual error in the section it replaced.

ADR 0003 also recorded why the alternative was rejected: "The conventions index forbids two files governing one artifact." That was true of the index as it stood. It is no longer. The template added an Extends relationship (`cfb9b39`, `e6bfc45`): a stack file may declare a base, "the derived file holds only the rules its stack changes — one rule, not the section around it — and the base governs every rule it does not."

Before deciding, the two files were compared rule by rule rather than argued about. `stack-kubebuilder.md` held 87 rules:

- **21 (24%) were identical to a `stack-go.md` rule** — §6 Naming was seven of seven, a verbatim restatement of the base's naming section.
- **14 conflicted** with a base rule and are genuine overrides.
- **52 (60%) had no counterpart** in a general Go service.

The reverse pass mattered as much: **38 base rules are inapplicable here**, naming files, directories, and tools a kubebuilder scaffold does not have — `internal/<domain>/service.go`, Layouts B and C, root-level `httpserver.go`, mockery, testify, `tests/` at the module root.

The first reading of the Extends text put the unit at the section, and under that reading this measurement argued against reversing: no section had zero identical rules, so holding any section meant copying the base rules inside it, which the index separately forbids. That reading was reported upstream as a defect and the text was corrected to the rule (`e6bfc45`) — which is what makes the reversal cost nothing in copied lines.

## Decision

`stack-kubebuilder.md` declares `stack-go.md` as its base in the index's Extends column. `stack-go.md` is restored from the template and is not edited here. `stack-kubebuilder.md` keeps only the rules that change a base rule or have no base counterpart; the 21 duplicates are deleted.

## Consequences

Easier: the 21 duplicated rules are gone and cannot drift. A base improvement reaches this repository by syncing one file, which is what `2f5871e` could not do. `stack-kubebuilder.md` is shorter and every rule in it now answers "what does the scaffold decide differently", which is a question with a checkable answer.

Harder: a contributor opens two files instead of one, and about 38 base rules describe a layout this project does not have. They are inert rather than wrong — a rule naming `internal/<domain>/service.go` never fires where no such file exists — and one derived rule, "the scaffold owns the top-level shape", is what settles the layout question against all of them. But a reader still passes them.

Not decided here: whether the template adopts a `stack-kubebuilder.md` of its own. That is the template PM's call, and this repository's file is being supplied to them as analysis regardless of what this ADR decides.

Ruled out: keeping ADR 0003 and paying the duplication permanently, now that the mechanism that made duplication necessary has been removed.
