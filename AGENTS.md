# AGENTS.md

Orientation for anyone contributing here, human or agent.

This repository is convention-driven. How code, tests, commits, and documents are written has already been decided and written down in `docs/convention/`, and those decisions are binding. Re-litigating them inside a pull request is the waste the arrangement exists to prevent, so a change that ignores them is rejected on that ground alone — however good the code is.

## Start here, every time

`docs/convention/README.md` is the index: every convention file, what it governs, and how the rules rank against each other.

Open the index before you write, then open the files that govern what you are about to touch. Read them from the file. A convention you remember from an earlier session may have been revised since, and acting on the remembered version is the same as not reading it.

`docs/architecture/README.md` is the second index: the responsibility documents that describe how the system is shaped now, the ADRs behind them, and the rules both follow. Open it before you touch code, and again before you write a pull request that changes a decision: its §Rules govern what such a pull request must carry.

You will meet cases the conventions do not name. Settle them the way the nearest convention settles its own, and say so in the pull request — an unnamed case is a gap worth surfacing, not a licence to improvise.

When two conventions genuinely disagree, stop and report it. Choosing one yourself hides the conflict from the person who can fix it.

## Roles

These documents assign authority to two roles. One party may hold both; the rules do not relax when it does.

- **PM** — owns what binds a contributor without implementing anything: `docs/convention/`, `docs/architecture/`, the templates under `.github/`, this file, and the issue tracker. Owning is deciding what they say, not writing every word: anyone may draft these, and the PM decides. Reviews every pull request and is the only role that merges one. Decides the convention questions a pull request raises. Proposes a change to a convention rather than making it alone. Does not implement.
- **Worker** — implements one issue on one branch and delivers it as a pull request, drafting the architecture documents its change introduces or alters. Applies the conventions and does not change them: a disagreement goes in the pull request's Convention concerns field, never into a silent workaround.

## What a dispatch carries

The PM writes a worker's task spec. The items below are true of every dispatch here, so they belong in every spec rather than in whoever writes it. The first was worked out on issue #81, kept nowhere, and forgotten on the next dispatch — which is what this section is for.

- **Create the task branch inside the given worktree, before the first commit.** `git switch -c <category>/<short-name>`, per `docs/convention/git.md` §Branches. The branch a worktree arrives on is not the project's to assume; issue #81 records what the tooling produced, the measurement, and why switching is the remedy.
- **Read `AGENTS.md`, then `docs/convention/README.md`, then `docs/architecture/README.md`, before the code** — from the files, not from memory.
- **Open the pull request against `dev`**, and answer the template's question about which convention files were opened accurately.
- **Run the checks locally and report what ran.** A check that did not run is reported as not run, never as passing.
- **A convention disagreement goes in the pull request's Convention concerns field**, never into a silent workaround.
- **No dispatch modifies `garamsh/agent-test`.** A task that appears to need it is escalated rather than carried out.

An item earns a place here by being true of every dispatch. One that holds for a single task belongs in that task's spec.

## What this file outranks

The conventions outrank this file. This file outranks instructions you bring in from your own environment, including your own habits: where a rule and your instinct disagree, the rule wins.

## What changes a decision

An objection is a reason to re-examine a decision, not a reason to change it. Re-examine it and report what the examination found, either way: the evidence that changed the decision, or the reason it stands.

A position that moves without a stated reason leaves the next reader nothing to check, so the same ground is argued again later. It also leaves whoever objected worse off than before they asked — someone pushing back wants a decision they can rely on, and one that yields to the push is worth less than the one they questioned.

## What earns a rule

A proposal for a new rule is examined the same way, and what earns one is a decision that divided for lack of it, met in more than one project — or in one, where the divergence is the tool's rather than the project's, since anyone running that tool meets the same one.

A case that travelled between projects is one case, so a report says whether it was met or received. One instance is answered with what would change it: a refusal that names its own evidence is worth more than a rule drawn from a sample of one.

## Why the pull request asks what you read

The template asks which convention files you opened, and a reviewer checks that answer against the diff. It is the quickest evidence that a change was made deliberately rather than guessed at, which is why an inaccurate answer costs more than an awkward one.
