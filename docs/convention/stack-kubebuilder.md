# Kubebuilder — Operator Conventions

> What the kubebuilder scaffold decides differently: the paths the CLI owns,
> the code it generates, API types and their markers, reconcile behaviour, and
> the runner, logger, and commands the scaffold ships instead of the general
> Go ones.
>
> Checked against kubebuilder 4.15.0 (layout `go.kubebuilder.io/v4`, PROJECT
> version 3), controller-runtime 0.24.1, controller-gen 0.21.0, Kubernetes
> client libraries 0.36.0, and Go 1.26. A claim below that names no version
> holds for these.

## Contents
- 0. Folder and file naming
- 1. Directory layout
- 2. Generated code
- 3. API types
- 4. Controllers
- 5. Markers
- 6. Naming
- 7. Error handling
- 8. Logging and metrics
- 9. Comments and docs
- 10. Testing
- 11. Imports and dependencies
- 12. Verification commands

## 0. Folder and file naming

The scaffold owns the top-level shape. `cmd/`, `api/`, `internal/`, `config/`,
`test/`, `Dockerfile`, `Makefile`, and `PROJECT` are placed by the kubebuilder
CLI; do not rename or relocate them, because the CLI writes into those exact
paths on the next `create api` or `create webhook`.

`test/utils/` is the one exception to the banned-name list. It is scaffolded ground, rewritten by the
CLI, and renaming it desynchronizes the scaffold; leave it and do not read it
as licence for a new `utils` elsewhere.

## 1. Directory layout

- `cmd/main.go` — the manager entry point. No directory named for the binary:
  the CLI writes this exact path. What belongs in it here is flag parsing,
  scheme registration, manager construction, and the `SetupWithManager` calls.
- `api/<version>/<kind>_types.go` — one file per kind, holding its `Spec`,
  `Status`, the object, and its list type. This project is single-group, so
  types live directly under `api/<version>/`.
- `api/<version>/groupversion_info.go` — scheme registration for the group
  version. Scaffolded; do not hand-edit.
- `internal/controller/<kind>_controller.go` — one reconciler per kind.
- `internal/controller/suite_test.go` — the envtest bootstrap shared by the
  package's controller tests.
- `internal/<concern>/` — logic that is not a reconciler, named for what it
  owns. A concern moves here from a controller file once it has its own tests
  or a second caller.
- `config/` — kustomize bases and overlays. Partly generated (§2), partly
  hand-edited.
- `test/e2e/` — end-to-end tests, run against a real cluster.
- `dist/` — what the entry points build rather than what a contributor writes:
  the installer manifest and the kustomize overlay carrying `IMG`. Ignored,
  never committed, and rebuilt from scratch, so a target may write here rather
  than editing a tracked manifest.

A reconciler that outgrows one file splits by responsibility
(`agent_controller.go`, `agent_pod.go`, `agent_status.go`), not by layer.

## 2. Generated code

These are produced by `make manifests generate` and are never hand-edited:

| Path | Produced by |
|---|---|
| `api/<version>/zz_generated.deepcopy.go` | `controller-gen object` |
| `config/crd/bases/` | `controller-gen crd` |
| `config/rbac/role.yaml` | `controller-gen rbac` |
| `config/webhook/manifests.yaml` | `controller-gen webhook` |

Rules:

- A change to an API type or an RBAC marker is committed together with the
  regenerated output, in the same pull request. CI fails when the working tree
  is dirty after `make ci`.
- `// +kubebuilder:scaffold:*` comments are anchors the CLI writes new entries
  into. Never delete, reorder, or move one; a lost anchor makes the next
  `kubebuilder create api` scaffold silently incomplete.
- Editing generated output to fix a problem is the wrong layer. Change the
  marker or the type and regenerate.
- **Upgrade with `kubebuilder alpha update`.** It three-way merges a newer
  scaffold onto this project and reads `cliVersion` from `PROJECT`, so that
  field is kept current. `alpha generate` is not the upgrade path: it
  re-scaffolds in place and deletes everything except `.git` and `PROJECT`.
- **The scaffold carries its own `AGENTS.md`.** kubebuilder writes one into
  every project. This repository's `AGENTS.md` is the contribution contract and
  outranks it. An upgrade that offers to replace that file is refused; its other
  changes are taken normally.
- **An upgrade re-adds `// +build` lines, and they are deleted again.** The CLI
  writes both build constraints into every tagged file, and the second is dead
  on every Go version this project supports. `make lint` reads tagged files and
  rejects it, so an upgrade taken whole fails the check set. Delete the lines
  rather than excluding the rule: an exclusion would quiet the entry point about
  the files it was just taught to read.

## 3. API types

- **Spec is desired state, Status is observed state.** Never store user intent
  in Status, and never read Status as an input to the decision Reconcile makes.
  A field that both sides write is a defect.
- **Every field carries a doc comment.** It becomes the `description` in the
  CRD and is what a user of the API reads; it is the one comment that is part
  of the published surface.
- **Follow the scaffold's tag style.** `metav1.ObjectMeta` and `Status` take
  `json:",omitzero"`; `Spec` is a non-pointer carrying no omit tag and marked
  `+required`. Optional fields are marked `+optional`, with a pointer where the
  zero value is meaningful. This is not the general Go struct-tag habit, and
  matching it keeps hand-written types indistinguishable from generated ones.
- **Validation belongs in markers** (§5), not in Reconcile, wherever a marker
  can express it. A rejection the API server can make never reaches a
  controller.
- **Status conditions are scaffolded, not added.** The generated `Status`
  already carries `Conditions []metav1.Condition` with `+listType=map` and
  `+listMapKey=type`. Keep those markers — they are what makes the list
  server-side-apply safe — and write the slice only through
  `meta.SetStatusCondition`, never by appending.
- **A second write goes through the object the first one updated, not a copy
  taken before it.** A write bumps the resource version and the client writes
  the new one back into the object it was given, so writing twice through that
  object succeeds; a copy held from before the first write carries the old
  version and is refused. The rule is about holding two copies, not about
  writing twice — re-read only when the copy in hand predates a write.
- **Status carries `observedGeneration`**, set to the object's `Generation` at
  the end of a successful reconcile, so a stale status is detectable.
- An API version is immutable once released. A change that would break an
  existing object is a new version, never an edit.

## 4. Controllers

- **Reconcile is level-triggered and idempotent.** It reads the world as it is
  now and drives it toward Spec. It never depends on which event woke it, how
  many events arrived, or what happened last time.
- **Running Reconcile twice on the same state changes nothing the second time.**
  This is the property every other rule here protects.
- **Never sleep.** To retry later, return `ctrl.Result{RequeueAfter: d}`. A
  blocked worker starves every other object of the same kind. `Requeue: true` is
  deprecated and is not the alternative.
- **`SetupWithManager` names the controller.** The scaffold emits
  `.Named("<kind>")`; keep it. A kind may carry more than one controller
  (`create api --controller-name`, recorded as a `controllers:` array in
  `PROJECT`), and the name is what separates them in logs and metrics.
- **Return the error.** controller-runtime's backoff is the retry mechanism;
  swallowing an error to keep the queue quiet hides a stuck object.
- **A missing object is not an error.** `apierrors.IsNotFound(err)` returns
  `ctrl.Result{}, nil` — the object is gone and there is nothing to reconcile.
- **Neither is a failure that retrying cannot fix.** A spec the API server
  accepted but this controller cannot act on — an image reference that does not
  parse, a field naming a kind this controller does not support — returns
  `ctrl.Result{}, nil` with the reason on a status condition. Returned as an
  error it requeues forever, and the only account of why the object is stuck
  sits in the manager's log rather than on the object the user can read. The fix
  is a spec edit, which wakes the controller on its own.
- **A reference to something absent is not that case.** A spec naming a Secret
  the user has yet to create fails the same way and reads the same on the
  object, but creating that Secret is not a spec edit and wakes nothing — return
  `nil` and the object stays stuck forever. Watch the referenced kind with
  `Watches()` and map the event back to the owner. Once the arrival wakes the
  controller, it is the case above and returns `nil` too.
- **Owned objects carry an owner reference.** Set it with
  `controllerutil.SetControllerReference` and watch the kind with `Owns()`, so
  deletion cascades and changes wake the owner.
- **Finalizers only for cleanup the garbage collector cannot do** — an external
  resource, something outside the cluster. In-cluster children are cleaned up by
  owner references. A finalizer that is added must be removable on every path,
  or the object cannot be deleted.
- **Status is written through the status subresource** (`.Status().Update` or
  `.Status().Patch`), never through a plain object update.
- **One reconciler per kind.** Two controllers writing one kind's status race.

## 5. Markers

- **RBAC markers sit on the `Reconcile` method** of the controller that needs
  the permission, and name only the verbs it actually calls. `make manifests`
  regenerates `config/rbac/role.yaml` from them; widening the generated role by
  hand is a §2 violation and is reverted on the next generate.
- **A permission a controller does not exercise is removed.** The manager runs
  with the union of every marker in the tree.
- **Emitting an event needs RBAC on `events.k8s.io`, not the core group.** The
  recorder moved to `k8s.io/client-go/tools/events`; a marker naming the core
  group grants nothing.
- **CRD validation markers sit on the field they constrain** — `+kubebuilder:validation:*`
  for bounds and formats, `+kubebuilder:default` for defaults,
  `+kubebuilder:printcolumn` on the type for `kubectl get` output. Cross-field
  constraints belong here too, not in Reconcile: `ExactlyOneOf`, `AtLeastOneOf`,
  `AtMostOneOf`, `+k8s:immutable`, and CEL through `+kubebuilder:validation:XValidation`.
- **A field with no schema node of its own takes the marker on the type, and the
  message names the field.** `metadata` is the case: the CRD carries no schema
  for it to constrain, and `fieldPath` pointing into it makes the API server
  reject the CRD outright. A rule on the type reaching `self.metadata.name` is
  the only placement available, so the message has to say which field it is
  about — nothing else in the refusal will.
- A marker's effect is verified by reading the regenerated file, not by
  assuming the marker parsed.

## 6. Naming

- **A kind's Go type name matches its CRD kind exactly.** The type is what
  `controller-gen` reads the kind from, so the two cannot drift.
- **Condition type and reason strings are constants**, never string literals at
  the call site. They are matched by `meta.FindStatusCondition` and read by
  users in `kubectl describe`; a typo in a literal creates a second condition
  instead of failing.

## 7. Error handling

- **Opaque is the usual answer here.** Reconcile's caller is controller-runtime,
  which does not branch on which failure it got, so an error leaving a
  reconciler normally publishes nothing to match on. A sentinel or a typed error
  earns its place when something inside this module branches on it.
- **The client wrapping a `client.Client` call keeps `%w`.** `apierrors.IsNotFound`
  and its siblings reach through a wrap, by `errors.As` on `APIStatus`
  (`k8s.io/apimachinery/pkg/api/errors/errors.go:818`); wrapping an API error
  with `%v` silently turns every `IsNotFound` check above it false, and a
  controller that treats a deleted object as a hard failure requeues forever.
- **The manager is the outermost boundary.** Reconcile returns and the manager
  logs once, with the controller name, the object, and the reconcile ID
  attached. There is no transport translator here and `main` never sees the
  error — the manager is the whole of it.
- **A recovered panic is indistinguishable from a transient failure.**
  controller-runtime recovers a panic in Reconcile unless `RecoverPanic` is set
  to false: it defaults to true
  (`sigs.k8s.io/controller-runtime/pkg/config/controller.go:56`) and the panic
  becomes `fmt.Errorf("panic: %v [recovered]", r)`
  (`pkg/internal/controller/controller.go:203`) — an ordinary returned error,
  which §4 then governs. A nil-map write therefore loops instead of crashing,
  and reads like a network blip. Parse and check rather than asserting.

## 8. Logging and metrics

- **Use the logger from the context**: the scaffold imports
  `logf "sigs.k8s.io/controller-runtime/pkg/log"` and calls `logf.FromContext(ctx)`.
  Keep the alias. The returned logger carries the controller name, the object's
  namespace and name, and the reconcile ID; constructing a fresh one drops that
  correlation.
- **`logr`, not `log/slog`.** controller-runtime's logging is `logr` end to
  end, and `cmd/main.go` installs a zap-backed implementation.
- **Structured key-value pairs, balanced.** `logcheck` is enabled in
  `.golangci.yml` and fails an odd argument list.
- Levels: `Info` for state changes a user would want to see, `Error` for a
  failure this code handles instead of returning — the manager logs a returned
  error itself (§7) — `V(1)` and deeper for per-reconcile detail.
- The reconcile path is hot. A log line per reconcile per object is a cost;
  log transitions, not steady state.
- **controller-runtime owns the registry; do not make one.** A collector is
  registered into `metrics.Registry`
  (`sigs.k8s.io/controller-runtime/pkg/metrics/registry.go:30`), because that is
  the only registry the manager's `/metrics` serves
  (`pkg/metrics/server/server.go:221`). Registering into a registry of your own
  succeeds and exports nothing — the metric exists, the code looks right, and
  the endpoint never shows it.

## 9. Comments and docs

- API field comments are published (§3) and are written for the API's user.
- Inline comments earn their place on a non-obvious invariant or an ordering
  constraint — not on paraphrase.
- `// TODO(name): …` with an owner, never `FIXME` or `XXX`.

## 10. Testing

| Layer | Runs against | Location |
|---|---|---|
| Unit | nothing | next to the source |
| Integration | envtest — real `kube-apiserver` and `etcd` binaries | `internal/controller/` |
| E2E | a Kind cluster | `test/e2e/` |

- **A controller package's tests are internal**, in the package under test and
  not in a `_test` package beside it. The scaffold's `suite_test.go` holds the
  `k8sClient` and the environment its sibling files use, and package-level state
  shared across files cannot cross a package boundary. This is the layout the
  CLI writes and the one `create api` writes into again.
- **Integration tests use envtest, not a fake client.** A fake client does not
  run defaulting, validation, or the status subresource, so it proves less than
  it appears to.
- **envtest has no kubelet.** Pods created at this layer stay `Pending` forever.
  A test asserting a Pod reached `Running` belongs in e2e.
- **Ginkgo v2 and Gomega** are the runner and matcher library for integration and
  e2e tests; `ginkgolinter` is enabled in `.golangci.yml`. A pure helper with no
  cluster interaction may use stdlib `testing` directly.
- **E2E tests sit behind `//go:build e2e`.** `go test ./...` does not run them,
  and `make test` excludes the directory as well; only `make test-e2e` does. A
  test that needs a cluster and is not behind the tag will fail every ordinary
  run.
- **Assert on observed state, not on calls.** Read the object back and assert
  its Spec and Status; do not assert that a client method was invoked.
- **Eventual assertions use `Eventually`** with an explicit timeout. Reconcile
  is asynchronous, and a bare read races it.
- **Idempotence is a test case.** Reconcile twice and assert the second call
  changes nothing.
- Test names read like a spec and say what broke without opening the file.

## 11. Imports and dependencies

- The Kubernetes libraries move together. `k8s.io/api`, `k8s.io/apimachinery`,
  `k8s.io/client-go`, and `sigs.k8s.io/controller-runtime` are upgraded in one
  change, never individually. They share generated types and a client contract,
  so a lone bump compiles and then fails at runtime on a type it did not expect.
- `ENVTEST_VERSION` and `ENVTEST_K8S_VERSION` are derived from `go.mod` by the
  Makefile. Do not pin them by hand; change the module version instead.

## 12. Verification commands

These sit behind the project's entry-point names.

| Task | Name |
|---|---|
| Lint (includes gofmt and goimports checks) | `make lint` |
| Format | `make fmt` |
| Test | `make test` |
| Build | `make build` |
| Whole check set | `make ci` |
| E2E (needs Kind) | `make test-e2e` |
| Regenerate manifests and deepcopy | `make manifests generate` |
| Run against the current kubecontext | `make run` |

`make ci` is what CI invokes. Run it before pushing.
