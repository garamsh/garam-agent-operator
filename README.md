# garam-agent-operator

Runs `sherlock` agents on a gaon cluster for our own platform: a control service holds each agent's desired definition, and the gaon operator runs each agent as a MicroVM from it.

## Contents
- Scope
- What it does
- Status
- Images
- Requirements
- Running the checks
- Running the services
- Rules

## Scope

This project runs only on our own system. It is not a product customers install, and nothing in this file is install instructions for one. BYO operator is on hold, and the code keeps the seams that would allow it ([ADR 0079](docs/architecture/adr/0079-run-only-on-our-own-system-and-keep-the-seams-that-allow-a-bring-your-own-operator.md)).

## What it does

The repository builds these binaries:

| Binary | Code | What it does |
|---|---|---|
| Control service | `cmd/control/main.go`, `internal/definition/`, `internal/console/`, `internal/distribution/`, `internal/execution/` | Holds each agent's desired definition in its own PostgreSQL store, and serves the console's routes, the controllers' routes and the agents' routes over it |
| Gaon operator | `cmd/gaon-operator/main.go`, `internal/gaon/` | Reads the control service's feed as its registered controller and reconciles each agent into a MicroVM on a gaon cluster |
| Guest supervisor | `cmd/gaon-guest/main.go`, `internal/gaon/guest/` | Runs inside the MicroVM and starts the agent |
| Renderer | `cmd/gaon-render/main.go` | Renders an agent's configuration for the guest |

`docs/architecture/control.md` and `docs/architecture/gaon-operator.md` are the record of each. `internal/certificate/`, `internal/garammachine/`, `internal/machinetoken/` and `internal/secretref/` are below the control service's domains, and `internal/gaon/` holds eight sibling packages (`docs/architecture/structure.md`).

Each service depends on `garam`. It authenticates under a key `garam` issued and exchanges that key for a token ([ADR 0072](docs/architecture/adr/0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md)):

- **The control service** calls `garam`'s machine listener at `--garam-machine-url`, presenting its own token (`cmd/control/main.go`, `internal/garammachine/`, `internal/machinetoken/`).
- **The gaon operator** calls `garam`'s machine listener at `GARAM_MACHINE_URL` and the control service at `CONTROL_URL` (`docs/architecture/gaon-operator.md` §Settings).

It is not the agent: `sherlock` lives in a separate repository.

## Status

This repository once held a Kubernetes manager that reconciled an `Agent` kind into a StatefulSet. [ADR 0083](docs/architecture/adr/0083-remove-the-kubernetes-manager.md) removed it, and the control service and the gaon operator are what remain. The gaon operator is not deployed from this repository, and `make operator-image`, `make guest-image`, `make pair-run-gaon` and `make guest-run` have not been run in CI (`docs/architecture/gaon-operator.md` §What is not built).

## Images

The control service's image is published to one repository, holding its release and development images told apart by tag ([ADR 0078](docs/architecture/adr/0078-publish-each-image-to-one-repository-and-tell-release-from-development-by-tag.md)):

| Image | Dockerfile | Repository |
|---|---|---|
| Control service | `build/control.Dockerfile` | `garam-agent-operator/control` |

A deployment references the image as `<repository>:<tag>@sha256:<digest>`. `docs/architecture/delivery.md` states the tag forms, how the image is published, and whether the repository exists yet. This file claims no tag in it. `gh release list -R garamsh/garam-agent-operator` lists the releases. The gaon operator's images are built by `make operator-image` and `make guest-image` and published by nothing.

## Requirements

- Go — the version in `go.mod`
- Docker, or another container tool set through `CONTAINER_TOOL`

The Makefile downloads golangci-lint into `bin/` on first use; it is not installed system-wide.

## Running the checks

`make ci` runs the whole check set — module tidiness, the lint configuration, lint, format, test, build, the image build context, and the action pins. An untidy `go.mod` or `go.sum` fails it first, with the fix named: run `go mod tidy` and commit what it changes. It is what CI invokes, and what to run before pushing. `make help` lists every target.

End-to-end tests need Docker and are not part of that set. `make test-e2e-control` runs the control service's suite against a PostgreSQL container and a real garam at `GARAM_REVISION`, and `make pair-run` runs the gaon operator against that garam and the control service, with a stand-in for gaon's API. garam's files are copied out of its private test image in ECR, pinned by digest, which needs an ECR login that can pull it. Where that login is not available, `make garam-e2e-from-source test-e2e-control` builds them from `garamsh/garam` instead, with git credentials that can read it.

CI runs `make ci`, `make test-e2e-control` and `make pair-run` on the promotion of `dev` to `main` and nothing else: a pull request into `dev` gets no automated check. So run all three before pushing, and report what ran in the pull request — that report is what a reviewer reads. `docs/architecture/integration.md` states which checks run where.

## Running the services

Our own deployment is not made from these commands: the control service's manifests belong to the environment repository (`docs/architecture/configuration.md`, `docs/architecture/delivery.md`).

The control service has no deploy target here. `make docker-build-control CONTROL_IMG=<image>` builds its image, and `make build` builds `bin/control`, `bin/gaon-operator`, `bin/gaon-guest` and `bin/gaon-render`. The control service needs `CONTROL_DATABASE_URL` and the flags `go run ./cmd/control --help` lists, and `docs/architecture/control.md` states what each is for. The gaon operator's settings are the environment variables `docs/architecture/gaon-operator.md` §Settings lists.

## Rules

Contributing to this repository means following the conventions in it, not general practice.

- `AGENTS.md` — the contribution contract, and who may change what
- `docs/convention/README.md` — the index of every rule that governs code, tests, commits, and documents
- `docs/architecture/README.md` — how the system is shaped now, and the decisions behind it
