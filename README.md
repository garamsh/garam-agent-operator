# garam-agent-operator

A Kubernetes operator that runs `sherlock` agent workloads in a cluster from a custom resource.

## Contents
- What it does
- Status
- Requirements
- Running the checks
- Running the operator
- Rules

## What it does

The operator's purpose is to take an agent's desired configuration, hold it as a custom resource, and reconcile the cluster until that agent is running as a Pod.

Longer term the configuration comes from `garam`, which also routes messages between agents and tracks their state. The two systems stay separate on purpose, so that someone else can attach their own operator to `garam` instead of this one. How that integration works — the endpoint, the credential, and which side opens the connection — is still open; the tracker carries it. Nothing in this repository depends on `garam` today.

Two things it is not: it is not the agent, which lives in a separate repository, and it is not the source of the configuration.

## Status

The controller builds an Agent's workload and reports what it observed. `api/v1alpha1/` holds the `Agent` types and `config/crd/bases/` the CRD they generate; `internal/controller/` reconciles an `Agent` into a StatefulSet it owns and records the outcome on the Agent's `Synced` condition, which `kubectl get agents` prints. Nothing here calls `garam`.

## Requirements

- Go — the version in `go.mod`
- Docker, or another container tool set through `CONTAINER_TOOL`
- `kubectl` and access to a cluster, for the deploy targets
- Kind, for `make test-e2e`

The Makefile downloads controller-gen, kustomize, setup-envtest, and golangci-lint into `bin/` on first use; they are not installed system-wide.

## Running the checks

`make ci` runs the whole check set — lint, format, test, build. It is what CI invokes, and what to run before pushing. `make help` lists every target.

End-to-end tests need a cluster and are not part of that set: `make test-e2e` creates a Kind cluster, runs them, and tears it down.

CI does not invoke both at every point. A pull request gets `make ci` and nothing else; `make test-e2e` runs when a change lands on `dev` or on `main`. So a green pull request says nothing about the end-to-end layer, and running `make test-e2e` before pushing is what closes that. `docs/architecture/integration.md` states which checks run where.

## Running the operator

Against the cluster in the current kubecontext, with the manager on your machine:

```sh
make install   # apply the CRDs
make run       # run the manager locally
```

Deployed into the cluster instead:

```sh
make docker-build docker-push IMG=<registry>/garam-agent-operator:<tag>
make deploy IMG=<registry>/garam-agent-operator:<tag>
```

`make build-installer IMG=<image>` writes a single applyable YAML to `dist/`. No image has been published yet, so `IMG` has no default worth using.

## Rules

Contributing to this repository means following the conventions in it, not general practice.

- `AGENTS.md` — the contribution contract, and who may change what
- `docs/convention/README.md` — the index of every rule that governs code, tests, commits, and documents
- `docs/architecture/README.md` — how the system is shaped now, and the decisions behind it
