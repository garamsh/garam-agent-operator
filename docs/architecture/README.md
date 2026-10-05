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
5. **Every domain or concern has a responsibility document.** The PR that introduces one adds its document; the PR that removes one deletes it. A system with no code yet has no more than `structure.md` (rule 6), and that is the correct state.
6. **Structural rules live in `structure.md`.** The shape chosen at bootstrap imposes rules — what the unit is, what may reference what, when a new one is earned — and they belong in the responsibility document for that concern, where a reviewer cites one by line and a correction is an ordinary edit. Not the ADR: rule 2 freezes that body, so superseding the choice of shape would be the price of fixing a rule's wording.
7. **A point the structural rules leave to the project is settled in an ADR** — the one recording the shape where the question is met at bootstrap, a later one where it is met later. Which side is taken decides what counts as a defect, so it is a decision under rule 4, and the ADR still holds the record after bootstrap. Nothing obliges a project to settle a question it has not met: until then the point waits under `structure.md`'s open questions, where it binds nothing, and moves into that document's current decisions when the project takes a side.
8. **Structural rules yield only to what a framework imposes.** Where `structure.md` and a convention file decide the same case differently, `structure.md` governs, unless the stack-specific file that applies decides it as its framework imposes it — the alternative fails to build or run, so the project cannot restate it. A stack file's spelling of a structural rule is not that: the rule it spells is `structure.md`'s. The shape chosen at bootstrap is this project's own refinement of what a stack-neutral file states for every project. A project with no `structure.md` meets no such conflict, and this rule asks nothing of it.

### What this file does not take from the template

`structure.md` landed in #222, so the template's rules 5 to 8 are taken here. The refusal recorded in PR #68, and re-examined on 2026-08-31, applied to a project with no `structure.md`, and that ground is gone. Two clauses are still not taken. Both name the template's `structures/` folder, which this project chose from (ADR 0038) and never holds:

| Not taken | Template commit | Why it cannot be true here |
|---|---|---|
| §Structure's `structures/` bullet | `d2675ce`, `65523e6` | Describes a folder this repository does not have. |
| Rule 4's `structures/` sentence | `d2675ce` (`convention-driven-project#276`) | Same folder. |

Before a delta between this file and the template is called stale, read it against this list and against this project's merged pull requests for the path.

## Index

### Responsibility documents

| Document | Covers |
|---|---|
| `agent.md` | The `agent.garam.sh` API group, the `Agent` kind, and its controller |
| `configuration.md` | How this operator's deployment is configured, and which repository owns each value |
| `control.md` | The control service: definitions and revisions, templates, profiles, creation and configure requests, their store, the console API that changes them, and the controller API that releases them |
| `delivery.md` | The images this project publishes, the reference a deployment uses, and where its output lands in a cluster |
| `structure.md` | What a unit of this code is, what may reference what, and when a new unit is earned |
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
| `adr/0007-claim-definitions-from-a-poller.md` | Claim garam's definitions from a poller beside the reconciler | accepted; composition held by garam superseded by ADR-0032; GRN never in spec superseded by ADR-0037 |
| `adr/0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md` | Renew the operator's credential into the Secret it is read from | accepted |
| `adr/0009-construct-a-claimed-agent-from-the-operators-own-configuration.md` | Construct a claimed agent from the operator's own configuration, and place the credential the claim admits it to | accepted |
| `adr/0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md` | Copy an agent's credential into a memory volume the Pod's own user owns | accepted |
| `adr/0011-the-conventions-template-is-the-frame.md` | The conventions template is the frame, and a divergence is earned by a fact | accepted |
| `adr/0012-declare-an-agents-tool-set-in-its-definitions-values.md` | Declare an agent's tool set in its definition's values, and carry the keys this operator knows into a file | accepted; declaration site in garam's values superseded by ADR-0032 |
| `adr/0013-the-base-carries-what-every-deployment-shares.md` | Carry what every deployment shares in the base, and an environment's values where that environment is reconciled | superseded by ADR-0017 |
| `adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md` | Keep the image repository immutable with a tag naming one commit, and reference the image by digest with the tag beside it | accepted |
| `adr/0015-run-the-e2e-suite-where-a-change-lands-and-advance-main-only-by-a-human-promotion.md` | Run the e2e suite where a change lands and not on every pull request, and advance `main` only by a human promotion | accepted; trigger decisions superseded by ADR-0028 |
| `adr/0016-report-what-the-operator-observed-and-stay-silent-where-it-observed-nothing.md` | Report to `garam` what this operator observed, stay silent where it observed nothing, and carry the epoch on the `Agent` it was proved at | accepted; epoch held in Status only superseded in part by ADR-0037 |
| `adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md` | Keep an environment's values where that environment is reconciled, and in an overlay here where nothing reconciles it | accepted |
| `adr/0018-keep-the-image-of-an-agent-this-operator-constructed-current-with-its-own-configuration.md` | Keep the image of an agent this operator constructed current with its own configuration | accepted |
| `adr/0019-mount-an-agents-tool-tree-from-an-image-this-operator-names.md` | Mount an agent's tool tree from an image this operator names, and point the agent at it | superseded by ADR-0027 |
| `adr/0020-enroll-this-operator-with-a-one-time-token-and-keep-the-key-it-generated.md` | Enroll this operator with a one-time token, and keep the key it generated | superseded by ADR-0021 |
| `adr/0021-present-any-one-enrollment-token-once-and-wait-for-another.md` | Present any one enrollment token once and wait for another, and end the enrollment on a certificate rather than on an attempt | superseded by ADR-0022 |
| `adr/0022-end-the-enrollment-on-a-certificate-that-has-not-expired.md` | End the enrollment on a certificate that has not expired, rather than on one this operator can read | accepted |
| `adr/0023-run-an-agents-workspace-as-a-second-container-this-operator-names.md` | Run an agent's workspace as a second container this operator names, and give it the user the Pod already names | accepted; the workspace on the state volume superseded by ADR-0044 |
| `adr/0024-write-an-agents-config-file-from-an-init-container-into-a-directory-this-operator-names.md` | Render a declared tool set into the Pod through an init container that writes a config file into a directory this operator names | accepted |
| `adr/0025-generalize-the-agent-kind-by-agent-type.md` | Generalize the `Agent` kind by `spec.type`, with a closed enum and a per-type dispatch table in the controller | accepted; descriptor fields extended by ADR-0029 |
| `adr/0026-single-ecr-repository-dev-tag-by-role.md` | Publish one image to one ECR repository, with a `dev-<hash>` tag on bring-up builds and the commit-hash tag carrying the identifier | accepted; one-image scope superseded by ADR-0033 |
| `adr/0027-an-agents-tools-arrive-in-its-own-image-and-this-operator-mounts-no-tool-tree.md` | An agent's tools arrive in its own image, and this operator mounts no tool tree | accepted |
| `adr/0028-run-the-checks-and-the-e2e-suite-on-the-promotion-to-main-and-nothing-on-dev.md` | Run the checks and the e2e suite on the promotion to `main`, and nothing on `dev` | accepted |
| `adr/0029-route-every-agent-specific-name-in-the-pod-through-its-types-descriptor.md` | Route every agent-specific name in the Pod through its type's descriptor, and keep the operator's images out of it | accepted |
| `adr/0030-carry-the-operators-certificate-expiry-and-garams-refusals-as-metrics.md` | Carry the operator's certificate expiry and garam's refusals as metrics, under names an alert can depend on | accepted |
| `adr/0031-support-agents-declared-in-garam-on-one-cluster-of-the-sherlock-type.md` | Support agents declared in garam, on one cluster, of the sherlock type, and keep a hand-written `Agent` to development | accepted; §1 superseded by ADR-0032 |
| `adr/0032-own-an-agents-desired-definition-in-this-projects-control-service.md` | Own an agent's desired definition in this project's control service, and render every `Agent` carrying a GRN from it | accepted |
| `adr/0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md` | Publish the control service as a second image, to a repository of its own, under the rules every image here follows | accepted |
| `adr/0034-place-garams-adapter-as-a-native-sidecar-beside-every-agent-this-operator-constructed.md` | Place garam's adapter as a native sidecar beside every agent this operator constructed, and own its placement only | accepted |
| `adr/0035-carry-an-agents-model-and-ego-in-its-spec-and-render-them-through-its-types-descriptor.md` | Carry an agent's model and ego in its spec, and render them into the Pod through its type's descriptor | accepted |
| `adr/0036-persist-the-control-services-desired-state-in-its-own-postgresql-database.md` | Persist the control service's desired state in its own PostgreSQL database, through pgx v5 | accepted |
| `adr/0037-carry-an-agents-identity-in-its-spec-and-start-the-agent-under-it.md` | Carry an agent's identity in its spec, and start the agent under it | accepted |
| `adr/0038-partition-the-code-by-domain.md` | Partition the code by domain, and let a domain depend on a sibling through its surface in one direction | accepted |
| `adr/0039-serve-the-consoles-mutations-from-a-console-domain-over-the-definition-domains-surface.md` | Serve the console's mutations from a console domain over the definition domain's surface, and keep the request record with the revision it produced | accepted |
| `adr/0040-release-desired-state-to-controllers-from-a-distribution-domain-each-decision-proved-by-garam.md` | Release desired state to controllers from a distribution domain, each decision proved by garam, as a whole set on every answer | accepted |
| `adr/0041-join-garams-reply-instruction-to-the-ego-wherever-the-adapter-is-placed.md` | Join garam's reply instruction to the agent's ego wherever the adapter is placed | superseded by ADR-0045 |
| `adr/0042-fence-each-agent-pod-on-positive-evidence-its-writers-stopped-and-mint-an-adapter-only-placement-token.md` | Fence each agent Pod on positive evidence that its writers stopped, and mint an adapter-only placement token per placement | accepted |
| `adr/0043-pull-managed-agents-desired-state-from-the-control-service-and-keep-each-grn-on-one-source.md` | Pull managed agents' desired state from the control service in a domain of its own, and keep each GRN on one source by a marker set when its Agent is built | accepted |
| `adr/0044-give-an-agents-state-and-its-workspace-separate-claims.md` | Give an agent's state and its workspace separate claims, and replace an existing StatefulSet without losing either | accepted; the replacement of every shared-shape StatefulSet superseded in part by ADR-0047 |
| `adr/0045-give-garams-reply-instruction-as-an-operator-instructions-file.md` | Give garam's reply instruction as an operator instructions file, behind a switch until the deployed agent image takes one | accepted |
| `adr/0046-suspend-an-agent-from-a-field-a-person-owns-and-release-its-pod-only-on-the-writer-fences-evidence.md` | Suspend an agent from a field a person owns, and release its Pod only on the writer fence's evidence | accepted |
| `adr/0047-replace-a-shared-claim-statefulset-only-where-the-migration-is-turned-on.md` | Replace a shared-claim StatefulSet only where the migration is turned on, and report which agents still share | accepted |
| `adr/0049-give-a-managed-agents-adapter-the-control-services-settings-behind-a-switch.md` | Give a managed agent's adapter the control service's settings and its outbox, behind a switch until activation is served | accepted |
