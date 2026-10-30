# garam-agent-operator

Runs `sherlock` agents in a Kubernetes cluster for our own platform: a control service holds each agent's desired definition, and a manager reconciles it into a workload.

## Contents
- Scope
- What it does
- Status
- Images
- Requirements
- Running the checks
- Running the operator
- Rules

## Scope

This project runs only on our own system. It is not a product customers install, and nothing in this file is install instructions for one. BYO operator is on hold, and the code keeps the seams that would allow it ([ADR 0079](docs/architecture/adr/0079-run-only-on-our-own-system-and-keep-the-seams-that-allow-a-bring-your-own-operator.md)).

## What it does

The repository builds two binaries:

| Binary | Code | What it does |
|---|---|---|
| Manager | `cmd/main.go`, `internal/desired/`, `internal/controller/` | Pulls each agent's desired state from the control service, renders it into an `Agent`, and reconciles that `Agent` into a StatefulSet in the cluster |
| Control service | `cmd/control/main.go`, `internal/definition/`, `internal/console/`, `internal/distribution/`, `internal/execution/` | Holds each agent's desired definition in its own PostgreSQL store, and serves the console's routes, the controllers' routes and the agents' routes over it |

`docs/architecture/agent.md` and `docs/architecture/control.md` are the record of each.

Both binaries depend on `garam`. Each authenticates to it under a key `garam` issued, and exchanges that key for a token (`internal/machinetoken/`, [ADR 0072](docs/architecture/adr/0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md)):

- **The manager** calls `garam`'s machine listener at `--garam-address`, and the control service at `--control-address`, presenting a token to each (`cmd/main.go`).
- **The control service** calls `garam`'s machine listener at `--garam-machine-url`, presenting its own token (`cmd/control/main.go`, `internal/garammachine/`).

It is not the agent: `sherlock` lives in a separate repository.

## Status

The manager builds agents only from the control service's feed, and with `--control-address` unset it builds none ([ADR 0077](docs/architecture/adr/0077-build-agents-only-from-the-control-services-feed-and-remove-the-legacy-path-and-the-cut-over.md)). A hand-written `Agent` is reconciled too, for development and bring-up only: it has no GRN, so `garam` does not see it (`docs/architecture/agent.md`).

`api/v1alpha1/` holds the `Agent` types and `config/crd/bases/` the CRD they generate. `internal/controller/` reconciles an `Agent` into a StatefulSet it owns and records the outcome on the Agent's `Synced` condition, which `kubectl get agents` prints.

## Images

Each image is published to one repository, holding its release and development images told apart by tag ([ADR 0078](docs/architecture/adr/0078-publish-each-image-to-one-repository-and-tell-release-from-development-by-tag.md)):

| Image | Dockerfile | Repository |
|---|---|---|
| Manager | `Dockerfile` | `garam-agent-operator/manager` |
| Control service | `build/control.Dockerfile` | `garam-agent-operator/control` |

A deployment references an image as `<repository>:<tag>@sha256:<digest>`. `docs/architecture/delivery.md` states the tag forms, how each image is published, and whether these repositories exist yet. This file claims no tag in them. `gh release list -R garamsh/garam-agent-operator` lists the releases.

## Requirements

- Go — the version in `go.mod`
- Docker, or another container tool set through `CONTAINER_TOOL`
- `kubectl` and access to a cluster, for the deploy targets
- Kind, for `make test-e2e`

The Makefile downloads controller-gen, kustomize, setup-envtest, and golangci-lint into `bin/` on first use; they are not installed system-wide.

## Running the checks

`make ci` runs the whole check set — module tidiness, lint, format, test, build, and the image build context. An untidy `go.mod` or `go.sum` fails it first, with the fix named: run `go mod tidy` and commit what it changes. It is what CI invokes, and what to run before pushing. `make help` lists every target.

End-to-end tests need Docker and a cluster and are not part of that set: `make test-e2e` runs the control service's suite against a PostgreSQL container and a real garam at `GARAM_REVISION` (`make test-e2e-control` alone), then creates a Kind cluster, runs the manager's, and tears it down. garam's files are copied out of its private test image in ECR, pinned by digest, which needs an ECR login that can pull it. Where that login is not available, `make garam-e2e-from-source test-e2e-control` builds them from `garamsh/garam` instead, with git credentials that can read it.

CI runs neither on `dev`: a pull request into `dev` gets no automated check, and both run only on the promotion of `dev` to `main`. So run `make ci` and `make test-e2e` before pushing, and report what ran in the pull request — that report is what a reviewer reads. `docs/architecture/integration.md` states which checks run where.

## Running the operator

These commands are for a contributor working against a development cluster. Our own deployment is not made from them: the manager's values and the control service's manifests belong to the environment repository (`docs/architecture/configuration.md`, `docs/architecture/delivery.md`).

Against the cluster in the current kubecontext, with the manager on your machine:

```sh
make install                                        # apply the CRDs
go run ./cmd/main.go --agent-copy-image=<image>     # run the manager locally
```

`make run` starts the same program with no flags, so it stops with `agent-copy-image is required`. With `--control-address` unset, as here, the manager builds no agents and reconciles only a hand-written `Agent`.

The manager deployed into the cluster instead:

```sh
make docker-build docker-push IMG=<registry>/<repository>:<tag>
make deploy IMG=<registry>/<repository>:<tag>
```

`docker-push` runs `make verify-image-ref` first, which, outside a release run, refuses a reference under `garam-agent-operator/` unless its tag is a 12-hex commit hash. `make build-installer IMG=<image>` writes a single applyable YAML to `dist/install.yaml`. `docs/architecture/delivery.md` states the procedure for publishing a development image.

The control service has no deploy target here. `make docker-build-control CONTROL_IMG=<image>` builds its image, and `make build` builds `bin/manager` and `bin/control`. It needs `CONTROL_DATABASE_URL` and the flags `go run ./cmd/control --help` lists, and `docs/architecture/control.md` states what each is for.

## Rules

Contributing to this repository means following the conventions in it, not general practice.

- `AGENTS.md` — the contribution contract, and who may change what
- `docs/convention/README.md` — the index of every rule that governs code, tests, commits, and documents
- `docs/architecture/README.md` — how the system is shaped now, and the decisions behind it
