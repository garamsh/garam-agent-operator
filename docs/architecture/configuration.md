# Configuration

How this project's binaries are configured, and which repository owns each value.

## Current decisions

- The operator is configured by flags on the manager's command line and by nothing else. `cmd/main.go` parses them; no file, ConfigMap, or environment variable carries a value a deployer chooses. `POD_NAMESPACE` and `POD_NAME` are the only environment variables and are not such values — the downward API supplies the Pod's own namespace, because the kustomize transformer that sets it does not reach into an argument, and its own name, which the image report reads the manager's own image off (ADR 0074).
- Every flag has exactly one owner, and there are two owners: this repository's base under `config/`, and the overlay that deploys that base. A flag neither sets runs at the default `cmd/main.go` declares, and leaving it unset is the base's decision.
- **The base owns a flag whose value is the same wherever this operator runs.** `config/manager/manager.yaml` sets it, and a deployment that wants another replaces the argument.

| Flag | Why the base owns it |
|---|---|
| `--agent-copy-image` | The manager refuses to start without it. The init containers that write an agent's config file and make its outbox, and a memory move's copy, run it. What it asks of the image is a shell, `install` and `mkdir`, and nothing of the agent, which no environment answers differently. |
| `--garam-private-key-file`, `--garam-client-id-file`, `--garam-token-url-file` | Paths inside the container, under the `mountPath` the same file writes. Each file's base name is the Secret key a person places that part of `garam`'s key issuance under — `private-key.pem`, `client-id` and `token-url` — so the three fix this operator's contract with whoever places the Secret. What differs between deployments is the key, not where this operator reads one (ADR 0072). |
| `--garam-trust-file` | The same, and it names the role rather than the certificate: what `garam`'s listener is verified against has already moved once, and only the file's contents moved with it. |

- **The deploying overlay owns a flag that names something one cluster holds.** The base sets none of these, and a base that set one would ship one environment's values to every other.

| Flag | Why the overlay owns it |
|---|---|
| `--garam-address` | Names a service in one cluster: where agents' tokens are minted, and what their adapters reach. Required where `--control-address` is set, and read nowhere without it ([ADR 0077](adr/0077-build-agents-only-from-the-control-services-feed-and-remove-the-legacy-path-and-the-cut-over.md)). |
| `--agent-image` | Names a registry, a repository and a build. `sherlock` publishes that image and owns its repository and tag scheme (`sherlock@b61451e:docs/architecture/adr/0020-immutable-image-repositories.md`), and which build a cluster runs is that cluster's, so the base cannot name one any more than `spec.image` can. |
| `--agent-workspace-image` | Names a registry, a repository and a build, as `--agent-image` does. Which build of the process serving an agent's files and commands a cluster runs is that cluster's. Unset is a Pod with no workspace, which is the correct state for a base with no environment behind it. |
| `--garam-issuer` | Names one deployment's garam, by the `machine.issuer` its chart is given, which is the `iss` its adapter signs messages with. It is also the switch: unset renders no `issuers` entry, which is the correct state for a base with no environment behind it, and for a cluster whose agents run a `sherlock` that refuses `keys-ca-file` (ADR 0071). |
| `--control-address` | Names the control service's API listener as one cluster reaches it, as `--garam-address` names garam's. It is also the switch: unset leaves this operator building no agents and making no outbound call, which is the correct state for a base with no environment behind it (ADR 0077). |
| `--control-trust-file` | A path inside the container to the root the control service's serving certificate chains to. No mount in the base carries that root yet, so the overlay that mounts one names the file. |
| `--control-grn` | Names the control service's operator, which `garam` registered in one organization, as the audience of the token this operator presents to it (ADR 0072). Required where `--control-address` is set. |
| `--agent-adapter-image` | Names a registry, a repository and a build of `garam`'s image, as `--agent-image` does of `sherlock`'s. Which build of the adapter a cluster runs is that cluster's, and it has to match the `garam` that cluster talks to. Unset is a Pod with no adapter, which is the correct state for a base with no environment behind it. |

- **The overlay for a cluster belongs in the repository that reconciles that cluster; where nothing reconciles it, the overlay lives here under `config/overlays/<environment>/`, and it is removed when something takes it.** A repository that reconciles a cluster does not thereby reconcile this operator in it, and `garamsh/gitops` did not until `garamsh/gitops#239` landed. It does now, so `admin@garam-dev`'s values are that repository's and this repository holds no overlay. Read on `admin@garam-dev` on 2026-09-05: an Argo CD `Application` named `gagent-operator` tracks this repository's `config/default` by path and revision, reports `Synced` and `Healthy`, and holds the manager's Deployment in its resource tree, where `argocd.argoproj.io/tracking-id` names that `Application`. On 2026-09-28 the `Application` tracking it is named `garam-agent-operator`. The annotation is what tells an owner from an applier; `app.kubernetes.io/managed-by: kustomize` is not, because Argo CD renders the same `config/` with the same kustomize and the label is present under either.
- **An image this project does not build is referenced the same way as one it does — `<repository>:<tag>@sha256:<digest>` — and where the registry holds no index, the platform manifest's digest is taken and the architecture it pins is recorded rather than left implicit.** That binds whoever sets `--agent-image`, `--agent-workspace-image` or `--agent-adapter-image`, in whichever repository holds the overlay: a reference with no index above it pins one architecture, so the overlay records which, and a node of another fails to pull rather than mismatching silently. `--agent-copy-image` is the base's and does have an index, so it carries the index digest instead.
- **What `config/` exposes so such an overlay can set those without restating the rest**, and what a change here may therefore not break:
  - `config/default` builds standalone, so a remote overlay or an Argo `Application` can reference it by path and revision.
  - The manager's Deployment carries one container, at index 0 of the Pod spec. `config/default/manager_metrics_patch.yaml` already addresses it that way.
  - The base sets none of them, so an appended argument is the only occurrence of a flag and no ordering decides which wins.
  - The manager's image is `controller:latest`, a name a consumer replaces rather than a registry this project claims. `make deploy` replaces it the same way, through a generated overlay under `dist/`.
- **No secret value is configured anywhere.** The three key flags and `--garam-trust-file` name paths the kubelet writes into from the `garam-credential` Secret the base mounts, and what those files hold is placed by a person from `garam`'s key issuance and reaches the process as files. This operator writes none of them and holds no RBAC on that Secret. Nothing an overlay sets carries either, and an overlay is not where one would be put.

### The control service, the gaon operator and the gaon chart

The Kubernetes operator above is the one this project releases. The control service is configured as follows, and the gaon operator, which is discontinued and not released (ADR 0086), only as far as its chart renders it.

- **The control service is configured by flags and one environment variable, and by nothing else.** `cmd/control/main.go` declares every flag and its default. No file, ConfigMap or other environment variable carries a value a deployer chooses. The one environment variable is `CONTROL_DATABASE_URL`, the store's connection URL, which is a variable rather than a flag because it carries a password. The service refuses to start without it, and so does `control publish-profile` (`control.md`).
- **This repository sets none of those values.** `deploy/charts/garam-agent-operator-gaon` is a chart of gaon kinds (ADR 0085) in which every value a deployment chooses is required and empty. A flag left unset runs at the default `cmd/control/main.go` declares.

| Flag | What it names | Default |
|---|---|---|
| `--health-probe-bind-address` | The address the health endpoints bind to | `:8081` |
| `--api-bind-address` | The address the console's, the controllers' and the agents' routes bind to, over TLS | `:8080` |
| `--api-certificate-file`, `--api-key-file` | The PEM files the routes are served under: the certificate chain and its private key | none |
| `--garam-machine-url` | The base URL of garam's machine listener | none |
| `--garam-server-root-file` | The PEM file holding the root the machine listener's certificate chains to | none |
| `--operator-private-key-file`, `--operator-client-id-file`, `--operator-token-url-file` | The three files of garam's key issuance for this service's operator, read at every exchange (ADR 0072) | none |
| `--console-origin` | A browser origin the console calls from; repeated for each. None set answers no CORS at all | none |

- **No secret value is a flag.** The database URL is the one value that carries a secret and it is an environment variable. The operator's private key is read from the file the flag names, never from the flag.
- **The gaon operator is configured by environment variables its `internal/gaon/config` reads**, and the table of them is in `gaon-operator.md`. This repository sets none of those values either.
- **A reference to an image the gaon chart runs, or starts, is `<repository>@sha256:<digest>`, with no tag.** gaon starts no other form, and the chart refuses a tag. This narrows the form `delivery.md` states for a gaon deployment: the tag stays beside the digest in the registry and in a record of the release, not in the value a gaon object carries.

### Which repository owns each value of the gaon chart

The chart's `values.yaml` lists every value with its meaning. Every value is required except `control.consoleOrigins`, `operator.guest.*` and a resource's `limits`, and a missing one fails the render with a message that opens with its name.

| Value | Owner |
|---|---|
| `image.control`, `image.operator`, `image.guest` (the guest is the operator's `AGENT_IMAGE`) | This repository publishes `image.control` (ADR 0033). It builds `image.operator` and `image.guest` with `make operator-image` and `make guest-image` and publishes neither, and the gaon operator is not released (ADR 0086). The deployment's repository, `garamsh/aurora`, records the digest it deploys |
| `garam.machineUrl`, `garam.tokenUrl`, and the file `garam.root` names | garam serves them; aurora supplies the address and places the root |
| `database.url` (the Secret and its key, the value is `CONTROL_DATABASE_URL`) | aurora creates the Secret and names it |
| `control.operatorKey`, `control.operatorClientId`, `control.operatorTokenUrl`, `operator.key`, `operator.grn`, `operator.controlGrn` | garam issues the keys and GRNs (ADR 0072); aurora places the files and names the Secrets |
| `control.certificate`, `control.key`, `control.consoleOrigins`, `operator.controlRoot` | aurora |
| `operator.gaon.url`, `operator.gaon.ca`, `operator.gaon.certificate`, `operator.gaon.key` | gaon's administrator issues the operator's client certificate; aurora names its Secret |
| `namePrefix`, `dnsZone`, `resources.*`, `operator.agent.cpu`, `operator.agent.memory` | aurora; this repository publishes no figure for either program |
| `operator.guest.machineUrl`, `operator.guest.controlUrl` | Optional: left unset, the operator defaults them (§gaon-operator.md) |
| The flags and environment variables, the paths files are written to, the ports, each Deployment's user, the probes | This repository, in the chart's templates |

The operator's `ClusterRole` and its binding to the client certificate's CN are gaon's administrator's, not rendered here (ADR 0085).

## Rationale

Which of the two owners a flag has is [ADR 0013](adr/0013-the-base-carries-what-every-deployment-shares.md)'s test, settled on issue #105 after the deployed configuration was measured against this repository: seven of the operator's eight project flags existed in no repository, and the Deployment's field managers were three imperative `kubectl` verbs on top of one apply.

Where the overlay itself lives is [ADR 0017](adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md), which supersedes ADR 0013 and carries that test forward unchanged. ADR 0013 sent an environment's overlay to the repository reconciling that environment and refused one here; issue #119 found that the repository it named does not reconcile this operator, which leaves an environment nothing reconciles with no home for its values at all. ADR 0017 gives that environment one here and takes it away again when a reconciler appears. It also settles the reference form for an image this project does not build, which [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md) and `delivery.md` decide only for the image it does.

That this operator's identity is a key `garam` issues and a person places — a Secret mounted read-only into the manager's Pod, read as files at every exchange and written by nothing here — is [ADR 0072](adr/0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md), which supersedes [ADR 0008](adr/0008-renew-the-operator-credential-into-the-secret-it-is-read-from.md)'s renewal into that Secret and removes the enrollment [ADR 0022](adr/0022-end-the-enrollment-on-a-certificate-that-has-not-expired.md) last decided. It is why four of the five project flags the base sets are the base's: they are the reading end of a mount `config/manager/manager.yaml` already writes. It is also why `--control-grn` is the overlay's: the token this operator presents to the control service is minted for that service's operator, which one organization registered.

That the image comes from the operator's own configuration rather than from a definition is [ADR 0009](adr/0009-construct-a-claimed-agent-from-the-operators-own-configuration.md). It settles that it is configuration at all; this document settles which repository types them. [ADR 0032](adr/0032-own-an-agents-desired-definition-in-this-projects-control-service.md) moves an agent's desired definition from `garam` to this project's control service and keeps the image out of it: the image stays operator configuration and is never a field a member edits. The storage sizes are the profile's, and ADR 0077 removes the flags that sized a constructed agent's claims, `--agent-storage-size` and `--agent-workspace-storage-size`, with the poll and report intervals, `--garam-poll-interval` and `--garam-report-interval`.

That the tool tree is not one of those values is [ADR 0027](adr/0027-an-agents-tools-arrive-in-its-own-image-and-this-operator-mounts-no-tool-tree.md), which supersedes [ADR 0019](adr/0019-mount-an-agents-tool-tree-from-an-image-this-operator-names.md) and removes `--agent-tools-image`. The agent's image carries its tools, so which tools a cluster's agents run is decided by the build `--agent-image` names, and no second flag is needed.

That none of these images is per agent type is [ADR 0029](adr/0029-route-every-agent-specific-name-in-the-pod-through-its-types-descriptor.md). `--agent-copy-image` asks nothing of the agent, so it serves every type. `--agent-image` and `--agent-workspace-image` name `sherlock`'s images because `sherlock` is the only type this operator builds. Whether they become per type is decided by the issue that implements a second type.

That the image an agent's workspace runs is a third is [ADR 0023](adr/0023-run-an-agents-workspace-as-a-second-container-this-operator-names.md), on the same test and with one difference worth reading: the image it names exists. Read from ECR on 2026-09-06, `garam/gagent-workspace:0.1.0` resolves to `sha256:ce3256dcd5ac46ceaa9d5e445da69998728b123f86bca92b4001185cfa0e6a7e`, which is an OCI image manifest and not an index — so the rule above applies in its second form, and the architecture an overlay setting it pins is `linux/amd64`.

That the gaon chart holds no value of a deployment's is [ADR 0085](adr/0085-ship-a-helm-chart-of-gaon-kinds-for-the-control-service-and-the-gaon-operator.md), and that it describes a program this project does not release is [ADR 0086](adr/0086-restore-the-kubernetes-operator-and-keep-the-gaon-operator-unreleased.md).

## Open questions

- **What names the operator image is outside this repository, and nothing here keeps it current.** The base leaves `controller:latest` for a consumer to replace, and the overlay replacing it for `admin@garam-dev` is `garamsh/gitops`'s. An operator image published here reaches that cluster when that overlay is written and not before, with nothing in this repository able to notice that it has not been — the gap `delivery.md` records for the manual publish path, one repository further away.
- **Whether the base should name the workspace image after all.** It is the overlay's by the test above and it is the one image of the three whose value would be the same everywhere today, because one build exists and no cluster has a reason to run another. The base naming it would ship one deployment's choice to every other the moment a second build appears, which is the ground the test rests on; leaving it with the overlay costs a value nobody has typed yet in any repository, so every agent `admin@garam-dev` runs carries no workspace. What would settle it is a second build, or a deployment that has run without one long enough for the log line to be the wrong place to say so.
