# Configuration

How this project's binaries are configured, and which repository owns each value.

## Current decisions

- **The control service is configured by flags and one environment variable, and by nothing else.** `cmd/control/main.go` declares every flag and its default. No file, ConfigMap or other environment variable carries a value a deployer chooses. The one environment variable is `CONTROL_DATABASE_URL`, the store's connection URL, which is a variable rather than a flag because it carries a password. The service refuses to start without it, and so does `control publish-profile` (`control.md`).
- **This repository sets none of those values.** It ships no manifest, base or overlay: where the control service runs and what it is given are the environment repository's. A flag left unset runs at the default `cmd/control/main.go` declares.

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

## Rationale

That the operator's identity is a key `garam` issues and a person places, read as files at every exchange and written by nothing here, is [ADR 0072](adr/0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md). It is why three of the control service's flags name files rather than values.

That the reference to an image is the digest with the tag beside it is [ADR 0014](adr/0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md), and `delivery.md` states the form for the image this project builds.

That this repository holds no deployment manifest follows [ADR 0083](adr/0083-remove-the-kubernetes-manager.md): the base and overlay it once held were the manager's, and the manager is removed. [ADR 0013](adr/0013-the-base-carries-what-every-deployment-shares.md) and [ADR 0017](adr/0017-an-unreconciled-environments-values-live-in-an-overlay-here.md), which decided who owned a flag of the manager's, end with it: ADR 0017 is superseded by it, and ADR 0013 was superseded by ADR 0017.

## Open questions

None.
