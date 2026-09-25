# Architecture

Current architecture of the system and the decisions behind it. This file is the index; workers read it before touching code, the PM keeps it honest.

## Structure

- **Responsibility documents** (one `.md` per domain or concern, e.g. `memory.md`, `gateway.md`) — the single source of truth for how the system is shaped *now*. To know the current state, read only these.
- **`adr/`** — the record of individual decisions: direction taken, context, rejected alternatives.

## Rules

1. **Responsibility document skeleton**: current decisions (consolidated) / rationale summary with links to the relevant ADRs / open questions. Do not copy ADR content — synthesize the present state.
2. **ADRs are append-only.** Once merged, the body is frozen. Exceptions: updating the status field (`accepted` → `superseded by ADR-XXXX`), fixing typos or broken links, and appending a dated entry under `## Errata` when the decision stands but a fact supporting it was wrong — the erratum names what falsified it, and the original text stays intact. A changed decision means a new ADR that supersedes the old one — never an edit.
3. **Decisions land in pairs.** A PR that adds or supersedes an ADR must update the affected responsibility documents in the same PR. A PR with only one of the two is rejected — the final state must always live in the responsibility documents.
4. Keep this index current: every responsibility document and every ADR is listed here. An ADR covers a decision that changes a project rule — a settled choice that does not affect the rules does not earn one.
5. **Every domain or concern has a responsibility document.** The PR that introduces one adds its document; the PR that removes one deletes it. A system with no code yet has none, and that is the correct state.

### What this file does not take from the template's §Rules

The template's §Rules holds eight rules; this file holds five. That difference is decided, not stale. Every clause below names `structure.md` or `structures/`, artefacts this project does not have, so each fails the first ground in [ADR 0011](adr/0011-the-conventions-template-is-the-frame.md): the template's text describes a shape this project does not have.

| Not taken | Template commit | Why it cannot be true here |
|---|---|---|
| Rule 4's `structures/` sentence | `d2675ce` (`convention-driven-project#276`) | Names a directory this project never had, past a bootstrap it has already run. |
| Rule 5's `structure.md` clause | `65523e6` (`convention-driven-project#287`) | Cites a rule 6 this file does not have, so taking it lands a dangling cross-reference. |
| Rule 6, structural rules live in `structure.md` | `65523e6` (`convention-driven-project#287`) | Its subject is a file this project does not have. |
| Rule 7, a point the structural rules leave to the project | `65523e6` (`convention-driven-project#287`) | Same subject. |
| Rule 8, structural rules rank between the convention tiers | `214546f` (`convention-driven-project#300`) | Its operative clauses govern `structure.md` against the convention tiers; the only sentence that holds here is the one saying the rule asks nothing of a project without that file. |

Rules 4 to 7 were refused in PR #68 on that ground. ADR 0011 postdates that PR and supplies a test it did not have, so the refusal was re-examined clause by clause against the test on 2026-08-31 and it stands. Rule 8 is not a re-examination: it landed upstream after `c7d9353`, the template commit #68 synced to, so it was never in front of that PR and is refused here for the first time. The same two commits also add and then revise §Structure's `structures/` bullet, which is not taken either, on the ground PR #68 gives for not taking the folder it describes.

Before a delta between this file and the template is called stale, read it against this list and against this project's merged pull requests for the path. The template's log alone cannot tell a change refused here from one never seen: an adapted file with unadopted upstream commits behind it looks the same either way.

## Index

### Responsibility documents

| Document | Covers |
|---|---|
| `agent.md` | The `agent.garam.sh` API group, the `Agent` kind, and its controller |
| `configuration.md` | How this operator's deployment is configured, and which repository owns each value |
| `delivery.md` | The image this project publishes, the reference a deployment uses, and where its output lands in a cluster |
| `integration.md` | How a change reaches `dev` and then `main`, and which checks run at each step |

### ADRs

| ADR | Decision | Status |
|---|---|---|
| `adr/0001-kubebuilder-go-v4-scaffold.md` | Build the operator on the kubebuilder go/v4 scaffold | accepted |
| `adr/0002-dev-integration-branch.md` | Integrate on `dev` and keep `main` for released state | accepted |
| `adr/0003-single-stack-convention-file.md` | Govern Go with one stack file written for the operator | superseded by ADR-0004 |
| `adr/0004-extend-stack-go.md` | Extend `stack-go.md` instead of replacing it | accepted |
| `adr/0005-statefulset-of-one.md` | Run an agent as a StatefulSet of one replica | accepted |
| `adr/0006-credential-group.md` | Carry credential access on a group, not on a user | accepted |
| `adr/0007-claim-definitions-from-a-poller.md` | Claim garam's definitions from a poller beside the reconciler | accepted |
| `adr/0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md` | Renew the operator's credential into the Secret it is read from | accepted |
| `adr/0009-construct-a-claimed-agent-from-the-operators-own-configuration.md` | Construct a claimed agent from the operator's own configuration, and place the credential the claim admits it to | accepted |
| `adr/0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md` | Copy an agent's credential into a memory volume the Pod's own user owns | accepted |
| `adr/0011-the-conventions-template-is-the-frame.md` | The conventions template is the frame, and a divergence is earned by a fact | accepted |
| `adr/0012-declare-an-agents-tool-set-in-its-definitions-values.md` | Declare an agent's tool set in its definition's values, and carry the keys this operator knows into a file | accepted |
| `adr/0013-the-base-carries-what-every-deployment-shares.md` | Carry what every deployment shares in the base, and an environment's values where that environment is reconciled | superseded by ADR-0017 |
| `adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md` | Keep the image repository immutable with a tag naming one commit, and reference the image by digest with the tag beside it | accepted |
| `adr/0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md` | Run the e2e suite where a change lands and not on every pull request, and advance `main` only by a human promotion | accepted |
| `adr/0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md` | Report to `garam` what this operator observed, stay silent where it observed nothing, and carry the epoch on the `Agent` it was proved at | accepted |
| `adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md` | Keep an environment's values where that environment is reconciled, and in an overlay here where nothing reconciles it | accepted |
| `adr/0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md` | Keep the image of an agent this operator constructed current with its own configuration | accepted |
| `adr/0019-mount-an-agents-tool-tree-from-an-image-this-operator-names.md` | Mount an agent's tool tree from an image this operator names, and point the agent at it | accepted |
| `adr/0020-enroll-this-operator-with-a-one-time-token-and-keep-the-key-it-generated.md` | Enroll this operator with a one-time token, and keep the key it generated | superseded by ADR-0021 |
| `adr/0021-present-any-one-enrollment-token-once-and-wait-for-another.md` | Present any one enrollment token once and wait for another, and end the enrollment on a certificate rather than on an attempt | superseded by ADR-0022 |
| `adr/0022-end-the-enrollment-on-a-certificate-that-has-not-expired.md` | End the enrollment on a certificate that has not expired, rather than on one this operator can read | accepted |
| `adr/0023-run-an-agents-workspace-as-a-second-container-this-operator-names.md` | Run an agent's workspace as a second container this operator names, and give it the user the Pod already names | accepted |
| `adr/0024-write-an-agents-config-file-from-an-init-container-into-a-directory-this-operator-names.md` | Render a declared tool set into the Pod through an init container that writes a config file into a directory this operator names | accepted |
| `adr/0025-generalize-the-agent-kind-by-agent-type.md` | Generalize the `Agent` kind by `spec.type`, with a closed enum and a per-type dispatch table in the controller | accepted |
| `adr/0024-write-an-agents-config-file-from-an-init-container-into-a-directory-this-operator-names.md` | Write an agent's config file from an init container, into a configuration directory this operator names | accepted |
