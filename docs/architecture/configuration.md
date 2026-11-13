# Configuration

How this project's binaries are configured, and which repository owns each value.

## Current decisions

- **The control service is configured by flags and one environment variable, and by nothing else.** `cmd/control/main.go` declares every flag and its default. No file, ConfigMap or other environment variable carries a value a deployer chooses. The one environment variable is `CONTROL_DATABASE_URL`, the store's connection URL, which is a variable rather than a flag because it carries a password. The service refuses to start without it, and so does `control publish-profile` (`control.md`).
- **This repository sets none of those values.** It ships `deploy/charts/garam-agent-operator-gaon`, a chart of gaon kinds (ADR 0085) in which every value a deployment chooses is required and empty: where the control service runs and what it is given are the environment repository's. A flag left unset runs at the default `cmd/control/main.go` declares.

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
- **An image reference a deployment carries is `<repository>:<tag>@sha256:<digest>`**, the form `delivery.md` states for the image this project publishes. An image this project does not build, one a deployment names, takes the same form: where the registry holds no index, the platform manifest's digest is taken and the architecture it pins is recorded rather than left implicit. A reference with no index above it pins one architecture, so a node of another fails to pull rather than mismatching silently.

- **A reference to an image the chart runs, or starts, is `<repository>@sha256:<digest>`, with no tag.** gaon starts no other form, and the chart refuses a tag. This narrows the form above for a gaon deployment: the tag stays beside the digest in the registry and in a record of the release, not in the value a gaon object carries.

### Which repository owns each value of the gaon chart

The chart's `values.yaml` lists every value with its meaning. Every value is required except `control.consoleOrigins`, `operator.guest.*` and a resource's `limits`, and a missing one fails the render with a message that opens with its name.

| Value | Owner |
|---|---|
| `image.control`, `image.operator`, `image.guest` (the guest is the operator's `AGENT_IMAGE`) | This repository publishes the images (ADR 0084). The deployment's repository, `garamsh/aurora`, records the digest it deploys |
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

That the operator's identity is a key `garam` issues and a person places, read as files at every exchange and written by nothing here, is [ADR 0072](adr/0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md). It is why three of the control service's flags name files rather than values.

That the reference to an image is the digest with the tag beside it is [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md), and `delivery.md` states the form for the image this project builds.

That this repository holds no Kubernetes manifest follows [ADR 0083](adr/0083-remove-the-kubernetes-manager.md): the base and overlay it once held were the manager's, and the manager is removed. The chart of gaon kinds that replaces them, and the reason it holds no value of a deployment's, are [ADR 0085](adr/0085-ship-a-helm-chart-of-gaon-kinds-for-the-control-service-and-the-gaon-operator.md). [ADR 0013](adr/0013-the-base-carries-what-every-deployment-shares.md) and [ADR 0017](adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md), which decided who owned a flag of the manager's, end with it: ADR 0017 is superseded by it, and ADR 0013 was superseded by ADR 0017.

## Open questions

None.
