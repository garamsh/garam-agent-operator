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
- A cluster running Kubernetes 1.33 or later. There CRD validation ratcheting is GA and needs no feature gate, and the operator relies on it: an `Agent` stored before a CRD validation rule was added is still updated by every write that leaves the field the rule reads unchanged (#289). The lab is measured on v1.36.3, with the gate on and not overridden (gitops, 2026-10-07).
- Kind, for `make test-e2e`

The Makefile downloads controller-gen, kustomize, setup-envtest, and golangci-lint into `bin/` on first use; they are not installed system-wide.

## Running the checks

`make ci` runs the whole check set — lint, format, test, build, and the image build context. It is what CI invokes, and what to run before pushing. `make help` lists every target.

End-to-end tests need Docker and a cluster and are not part of that set: `make test-e2e` runs the control service's suite against a PostgreSQL container and a real garam it builds at `GARAM_REVISION` (`make test-e2e-control` alone; fetching garam needs git credentials that can read `garamsh/garam`), then creates a Kind cluster, runs the manager's, and tears it down.

CI runs neither on `dev`: a pull request into `dev` gets no automated check, and both run only on the promotion of `dev` to `main`. So run `make ci` and `make test-e2e` before pushing, and report what ran in the pull request — that report is what a reviewer reads. `docs/architecture/integration.md` states which checks run where.

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
