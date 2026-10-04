# Review Conventions

What makes a PR review valid, and how authors respond. Applies to every review and every response; violations are grounds for rejection.

## Contents
- Submitting a review
- What each check verifies
- Citing violations
- Blockers and nits
- Single-account setups
- Reviewing your own change
- Responding to a review (author)

## Submitting a review

- **Reviews are formal.** Decisions are submitted as approve / request changes / comment via the review mechanism — never as bare comments. Review states gate merges; comments do not.
- **Every review body carries the evidence table**, approval included:

```
| Check | Result | Evidence |
|---|---|---|
| Scope | pass / fail / unverified | — |
| Conventions | … | rule file §section — diff file:line |
| Architecture | … | … |
| Documentation | … | … |
| Verification | … | … |
| Depth | … | … |

Decision: approve / request changes / reject
```

- An approve requires the Scope, Verification, and Depth rows to read `pass`. The remaining rows may read `unverified`, with a stated reason (some PRs touch nothing a convention governs). A `fail` in any row is a request-changes or a reject, never an approve.
- A check you did not verify is marked `unverified` — never guessed. Skipping a check is allowed; hiding the skip is not. A review full of `unverified` rows tells the reader the review did not really happen.
- **The evidence has to test the claim it is offered for**, in a table row or anywhere else in the review. A cheap instrument — a command, or a sentence quoted out of the section that scopes it — usually answers a question adjacent to the one asked, and a clean answer looks the same either way. A checksum answers whether a file changed; where the row asks whether a measured value changed, it is the wrong instrument even on every file where the two agree.
- **A running system reports what is in effect, not what was decided.** Where a claim is about a decision — a configured value, a chosen limit, a deliberate default — evidence read from a live object does not establish it: a tool that fills in defaults renders them indistinguishably from authored ones. Read the authored artifact, and say which one you read.
- **Confirm the checks belong to the head commit**, not merely that checks are green. Compare the PR's head ref with the commit the check suite ran on: green on an older commit says nothing about the head. A result the project expects and the head does not carry is not a pending state to wait through — it is an unverified change, reported rather than merged. Where the project expects none, nothing is missing.
- **A conclusion is not a verdict unless the run did the work.** Read what a run did before reading its conclusion: count the steps that ran, and the work those steps selected. With either count at zero, the run has tested nothing, whatever it reports — `failure` because it never started, `success` because every step was skipped or selected no work, `skipped` outright. Such a result leaves the row it would have filled `unverified`.
- **What stands in for a check has to be able to fail everywhere that check fails.** A stand-in's name reads as the whole while it covers a part, and both results read alike, so compare the two by coverage instead: enumerate what each reaches and record both in the row that rests on the stand-in — the command that ran against every task the claimed check runs, a changed value against every passage that states it. That last comparison includes the document that changed, because a diff does not fail where an unchanged passage in the same file still names the old value. Coverage the stand-in does not reach stays `unverified`.
- **Reject maps to request changes.** GitHub has no `reject` review state, so a reject decision is submitted as a request-changes review whose decision line reads `reject` (in single-account setups, the `comment` substitute per §Single-account setups). What reject does that request changes does not: the reviewer closes the PR and files an issue describing the right direction.

## What each check verifies

| Check | Fails when |
|---|---|
| Scope | The diff carries a line the stated task did not ask for. |
| Conventions | A changed line breaks a rule in a file that governs it. |
| Architecture | A decision or a domain changed without the documents that describe it, or a changed line contradicts what a responsibility document in `docs/architecture/` states. |
| Documentation | A change to structure, workflow, or conventions left an affected document stale. |
| Verification | The PR claims a check the reviewer cannot confirm ran. |
| Depth | The change obeys every rule and is still the wrong thing to keep. |

**Scope** is measured against the task the pull request states — its issue where one exists, its Summary where none does — never against taste. A line that traces to an acceptance criterion is in scope however large it is, and a correct one-line drive-by is out of scope however small. Out-of-scope work is not wrong — it is a separate issue.

**Depth** is the check the other rows cannot make. They ask whether the change follows the rules; Depth asks whether it is right: a correctness risk the tests do not cover, a materially simpler approach passed over, a defect no convention happens to name. Marking it `pass` asserts the reviewer looked for those and found none, so it is the one row that cannot be filled without reading the change itself.

## Citing violations

- A violation claim cites both sides: `rule file §section` and `diff file:line`.
- If you cannot cite a rule, it is a preference, and preferences never block a merge.
- A Depth finding is the exception the check exists for. It names no rule by definition, so it cites the diff and the concrete failure it produces instead — the input that breaks, the case the tests miss, the simpler shape passed over. That is citable, and it blocks.

## Blockers and nits

- Tag every requested change **blocker** or **nit**.
- A nit never blocks a merge. A blocker always states the expected fix direction — "wrong" without "do this instead" is noise.

## Single-account setups

When the PM and authors share one GitHub account, `comment` is the only review state GitHub accepts on one's own pull request — `approve` and `request changes` are both refused. Every decision is submitted as a `comment` review carrying the evidence table and its decision line, plus one line saying the state was substituted because GitHub allows no other. That comment is the review; it is not a bare comment.

- **Approve.** Post the review, then merge immediately — the merge is the approval.
- **Request changes.** Post the review and leave the PR open. The author responds to it as to any request-changes review.

## Reviewing your own change

A self-review reads the diff against the reviewer's own understanding of the rules — the understanding that produced the diff. It catches what the change contradicts on its face. It misses what the change contradicts in something the reviewer already believes, because reading the diff again does not re-examine the belief.

- **The review says the author reviewed it**, in one line beside the decision. A reader cannot tell otherwise, and a review that hides it is asserting a second pair of eyes.
- **What the shared authorship left unchecked is marked in the row that owns it.** A rule two sections away that the author already believed they knew is a Conventions row; a command whose output arrived after the claim was written is a Verification row. The work is finding them — `pass` asserted from the understanding that produced the diff is what this section exists to name.
- Nothing else relaxes. §Submitting a review holds for a self-review unchanged.

## Responding to a review (author)

- Address every blocker, or rebut it by citing a rule or path that supports your approach. Silent ignoring is not allowed.
- Answer nits too: fix, or state briefly why not.
- Respond on the same branch and the same pull request; never open a replacement.
