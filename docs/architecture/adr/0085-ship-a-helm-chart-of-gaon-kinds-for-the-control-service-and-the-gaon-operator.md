# ADR 0085: Ship a Helm chart of gaon kinds for the control service and the gaon operator

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #424, step 5 of #416. [ADR 0083](0083-remove-the-kubernetes-manager.md) removed every manifest this repository held, and [ADR 0084](0084-publish-the-gaon-operator-and-the-guest-image-from-the-release-workflow.md) published the three images. Nothing described how the control service and the gaon operator run on gaon, so the objects `gaon apply` takes were written where they were applied, by hand and unchecked.

gaon is not Kubernetes. It has no namespaces and resolves only `<service>.default.svc.<zone>`, so a short name is not an address. `gaon apply` takes gaon kinds only (`Service` of `compute.gaon.garam.sh/v1alpha1`, `Deployment` of `workloads.gaon.garam.sh/v1alpha1`), refuses an unknown field, and applies in Helm's order. `garamsh/garam` already ships a chart of this shape (commit `89eeaf0c`), rendered with `helm template <release> <chart> | gaon apply -f -`.

Which repository owns each value was settled before: this project sets no value a deployer chooses ([configuration.md](../configuration.md)). A chart must therefore hold none of aurora's, and must refuse to render without them.

## Decision

1. **The chart is `deploy/charts/garam-agent-operator-gaon`.** It renders three objects: the control service's `Deployment` and `Service`, and the gaon operator's `Deployment`. The operator has no `Service`: it serves health on 8081 to its probes and nothing else dials it. Helm is a template engine only; no Kubernetes kind is rendered, and neither a `Secret` nor a `ConfigMap` is created.

2. **The chart mirrors garam's.** Objects are named `<namePrefix>-<word>`; every address a process dials is the full `<service>.default.svc.<dnsZone>`; each object's `resources.requests` is required with no default; every refusal is made at render time by `required` or `fail`, and its message opens with the dotted name of the value, so it names what to set.

3. **Every value is empty in `values.yaml`, and none has a default.** Each is required except `control.consoleOrigins`, `operator.guest.*` and a resource's `limits`, which are optional. The chart holds no secret value, no address, host, account or digest of aurora's. A `secret` or `configMap` names an object the deployer creates, with the key in it, and exactly one of the two is set. A private key and the database URL name a `Secret` only. A file a process reads is written into its MicroVM through `files[].valueFrom`, owner-only (`0400`, written `256`) for a private key. gaon's `MicroVMFile` names no owner when none is given, so the file belongs to the MicroVM's user; each Deployment's `user` is the `USER` of the Dockerfile that builds its image, which the check compares.

4. **An image is `<repository>@sha256:<digest>`.** The chart refuses a tag, because gaon starts no other form. `image.guest` is not run by the chart: it is the composed guest image the operator starts for each agent, passed to it as `AGENT_IMAGE`, whose own pattern it shares.

5. **`make chart-check` is part of `make ci`.** `hack/chartcheck` discovers every chart under `deploy/charts`, renders each `ci/*-values.yaml`, and validates each rendered object against gaon's API types. lib/api has no `Deployment` type, so the program decodes the wrapper strictly itself and validates its template as the MicroVM every replica becomes, through lib/api's own `Validate`; a `Service` is validated as lib/api's `Service`. It then renders each `ci/refused/<value>[@variant].yaml` over the minimal values and requires a failure whose message opens with `<value>`. It scans the chart outside `ci/` for a PEM block, an image digest, a 12-digit account id, an AWS host and a URL with a password. It prints what it covered, with how the set was selected, and a count of zero is a failure.

6. **The gaon pin is `go.mod`'s, and only `go.mod`'s.** `hack/chartcheck` imports `github.com/garamsh/gaon/lib/api`, so moving the module moves the check, and the report prints the version it was built with. No second file states the pin. A `replace` of the module fails the check.

7. **Helm is pinned in the Makefile** as `HELM_VERSION`, installed into `bin/` by `go-install-tool` as every other tool is, and moved by the PM.

## Consequences

- **Not rendered, and owed by someone else.** The operator's access to gaon is a `ClusterRole` and a binding to the client certificate's CN `garam-gaon-operator`. gaon has those kinds, and the chart does not render them: they belong to whoever administers that gaon, because they grant authority over it. A `NetworkPolicy` is likewise a kind gaon offers at the pinned commit, and is not rendered; §What is not built of `gaon-operator.md` keeps it.
- **`control publish-profile` is not rendered, and the deployment runs it.** [ADR 0056](0056-publish-a-deployments-profile-versions-through-a-subcommand-of-the-control-binary.md) says a Job; gaon has no Job kind at the pinned commit. Its run-once object is a `MicroVM` with `runStrategy: Once`, which `gaon apply` takes. A MicroVM's `ValidateUpdate` refuses a change to any spec field but `runStrategy`, `idleTimeoutSeconds`, `resources` and `volumes` (`lib/api` `microvm.go` at the pin), and the profile files are `spec.files`, so applying a new profile under the same name is refused, and the same name applied again does not run again. Every publication therefore needs a MicroVM named for itself, which a chart rendered from stable values cannot supply, and the chart would also re-apply a finished run on every deploy. Whoever deploys, `garamsh/aurora`, runs it: a `MicroVM` of the control image, `command` `/control`, `args` `publish-profile --file <path>` for each file, `CONTROL_DATABASE_URL` from the database Secret, the files from its ConfigMap through `files[].valueFrom`, a name that differs for each publication. `control.md` §Who publishes one says so. A later chart that renders it would need a required value naming the run.
- A `*_FILE` value in the operator's environment names a path the chart fixes, so the Secret keys a deployer supplies are what vary, never the paths.
- A value this project does not know a figure for (resources, the agent's cpu and memory) is the deployer's and fails the render when absent, as the operator refuses to start without it.
- The chart's version is its own and is not a release of this project. A consumer takes the chart by a git ref and the images by digest.
- Moving the gaon module may fail `make chart-check` for a rendered object gaon now refuses; that is the check doing its job, and the chart changes with it.
- `configuration.md` says which repository owns each value; `gaon-operator.md` drops the deployment from what is not built.
