# ADR 0043: Pull managed agents' desired state from the control service in a domain of its own, and keep each GRN on one source by a marker set when its Agent is built

> Status: accepted; rendered fields extended by ADR-0054
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #217, first slice: the manager's consumer of the control service's desired feed. [ADR 0032](0032-own-an-agents-desired-definition-in-this-projects-control-service.md) moves an agent's desired definition to the control service and has the manager render each `Agent` from it, keeping every name the GRN derives. [ADR 0040](0040-release-desired-state-to-controllers-from-a-distribution-domain-each-decision-proved-by-garam.md) pins the wire, contract C2:

- `GET /v1/operators/self/desired` is level-triggered: every answer is the controller's whole releasable set, under a cursor that only wakes a long poll of up to 30 seconds. Revisions are canonical decimal strings.
- `POST /v1/operators/self/agents/{agent}/status` reports the observed and rendered revision.
- A 422 with kind `too_many_agents` is definite. A 500 or 503 is transient.

Before this slice, a constructed agent's values were applied only at its first construction.

**Which agents can reach the poller.** At the local `garamsh/garam` checkout `9afd0bf`, `api/machine.yaml:240-246` and `:1228-1235` say this of an agent `createManagedAgent` made:

- it is absent from every `listDefinitions` answer;
- claiming a definition never reaches it;
- `issueAgentCertificate` refuses its operator.

So the poller that builds agents from garam definitions never constructs a managed agent, and the control feed is the only path that names one. Agents built from garam definitions keep that source until garam#1171's per-GRN switch, which is out of this slice.

The PM settled the bindings on this dispatch:

1. the source marker;
2. construction of a managed `Agent`;
3. the rendered fields;
4. the key reference's format;
5. the error handling;
6. the status report;
7. the domain and the flags;
8. the cursor held in memory.

## Decision

**The consumer is a domain of its own, `internal/desired`.** It holds the C2 client, the `Puller` (a manager `Runnable`), the `Renderer` interface, and in `internal/desired/renderer/` the implementation that writes `Agent`s. On `structure.md` §A new domain:

- **Vocabulary and lifecycle.** Feed, cursor, revision, rendered and observed revision, refusal backoff. They are none of `internal/garam`'s, whose subject is garam's machine listener, nor `internal/controller`'s, whose subject is the workload.
- **Dependent outside.** The manager's composition site depends on its surface. The control service's C2 wire is the counterpart it is written against.
- **Stable surface.** `NewClient`, `NewPuller`, `Renderer` and the sentinels hold steady while what the renderer writes grows with the API.
- **Shared naming.** The GRN-derived names both sources build under moved below both domains to `internal/agentname` (`structure.md` §Shared code). `internal/garam/constructor.Name` now calls it.

**Each GRN takes its desired state from one source, and the `Agent` says which.** `spec.identity.source` is `Garam` or `Control`, and absent reads as `Garam`, which is every `Agent` built before the field existed.

- **Who writes it.** The poller's construction writes `Garam`. The feed's construction writes `Control`.
- **The rule.** A CEL rule refuses any update that moves `Control` to anything else, so the only transition is `Garam` (or absent) to `Control`, which is garam#1171's switch.
- **No status read.** The marker is in the spec, written when the `Agent` is built, and nothing reads status to learn a source (`stack-kubebuilder.md` §3).
- **The guard.** The renderer refuses to write an `Agent` whose source is not `Control`, or whose GRN differs, with `ErrNotControlSource`, and that is logged. No GRN is rendered from both sources.

**A GRN the feed names with no `Agent` is constructed as managed.**

- **Names.** It is created under `agentname.Agent(grn)`, with credentials Secret `agentname.CredentialsSecret(grn)`.
- **Spec.** Identity is `{grn, epoch from the feed, source: Control}`, and the image is `--agent-image`.
- **Credential.** None is placed. A managed agent's first certificate comes from `issueInitialCertificate` under control's `operationRef` (#218), so until #218 places one, the `Agent` waits as `CredentialsSecretMissing`.

**Every new revision is rendered into a `Control` `Agent`:**

- model, ego and tools;
- the profile's resources, storage size and storage class;
- `identity.assignmentEpoch`;
- the operator's image.

The image is the operator's configuration, never a revision's (ADR 0007). It is rewritten on each render because the image correction (ADR 0018) reaches only `Agent`s the poller constructed. A storage size or class the claimed volume cannot follow is reported on `Synced` (`StorageSizeImmutable`, and the new `StorageClassImmutable`), never forced. An unchanged spec is not written.

**The model's key reference is `<secret-name>/<key>`, in the `Agent`'s namespace.** Neither a Secret's name nor a data key can hold a `/`, so the split is unambiguous. A malformed reference, a model missing a field, or an unparseable storage size leaves that agent unrendered for that revision with `ErrMalformed`, which is logged, and no status is reported. The PM is filing a follow-up so the control service refuses a malformed reference when it is written.

**Errors:**

- **Transient.** A 500, a 503, a transport error or an undecodable answer is asked again after a wait that starts at 1 second and doubles to 30.
- **Refusals.** Any 4xx, including 422 `too_many_agents`, is definite and never asked again at once, but it can clear: a renewed certificate, a reissued grant, agents moved away. So the puller keeps asking every 5 minutes, and logs on a change of status and kind rather than on every attempt.
- **Metrics.** `garam_operator_control_refusals_total{route,status}` counts every refusal. `garam_operator_control_feed_refused` is 1 while the last desired answer was a 4xx and 0 otherwise, so an alert can fire on a refusal that does not clear.

**Status and cursor:**

- **Withheld agents.** An agent absent from an answer is withheld for now and left as it is. Nothing is deleted.
- **Status.** After each successful render, the puller reports `observedRevision` equal to `renderedRevision` equal to the revision. A refused report is logged and reported again with the next answer.
- **The cursor.** It is held in memory, because a request without one is answered at once with the whole set. A restart renders each agent once more, which writes nothing where the `Agent` already matches.

**Connection and flags:**

- **Client certificate.** The client presents this operator's own certificate pair: the files `--garam-certificate-file` and `--garam-key-file` name, read at each handshake, because the controller C2 authenticates is the operator garam registered.
- **Root.** It verifies the control service against `--control-trust-file`.
- **Switch.** The puller runs only when `--control-address` (a host and port) is set.
- **Shared TLS helper.** The composition site builds the TLS configuration with the same helper the garam client uses, because the pair is the same and only the root differs.

## Consequences

- **A managed agent's tool set, model, ego and profile follow its control revisions.** The first-construction-only gap ADR 0032 names is closed for agents on the control source. Agents on the garam source keep it until #1171 switches them.
- **A managed agent does not run yet.** It has no credential until #218.
- **A refusal is visible.** It shows in a gauge and a counter, and the puller recovers by itself when the refusal clears.
- **The image of a `Control` `Agent` is rewritten on each new revision.** A corrected `--agent-image` therefore reaches it at its next revision, or at the manager's restart.
- **Rendering never deletes.** An agent the feed stops naming, because it moved or its proof failed, keeps its `Agent` and workload. What becomes of it is placement and lifecycle (#218).

Ruled out:

- **Reading the source from status, or from whether `status.agent` is set.** That is a decision from status.
- **An annotation for the source.** It is untyped and editable both ways. The spec field carries a one-way rule the API server enforces.
- **Stopping the puller on a 4xx until a restart.** A refusal can clear, and a stopped puller needs a person to notice it.
- **Folding the consumer into `internal/garam`.** Its counterpart is the control service, not garam.
- **Importing the control service's `internal/distribution` types.** The manager imports nothing of the control service's binary (`control.md`), and the wire is the contract.
