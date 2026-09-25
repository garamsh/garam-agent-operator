# ADR 0001: Build the operator on the kubebuilder go/v4 scaffold

> Status: accepted
> Date: 2026-08-16

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

This project manages agent workloads on Kubernetes, which requires a custom resource, a controller that reconciles it, RBAC, and a deployable manager.

The repository was initialized with the kubebuilder CLI before these documents existed. `PROJECT` records `cliVersion: 4.15.0`, `layout: go.kubebuilder.io/v4`, and `version: "3"`; `go.mod` pins controller-runtime 0.24.1 and the Kubernetes client libraries at 0.36.0 on Go 1.26. kubebuilder 4.15.0 was the current release on the date above.

The scaffold is not a neutral starting point. It owns the directory layout, generates deepcopy functions and CRD, RBAC, and webhook manifests, writes `// +kubebuilder:scaffold:*` anchors it reads back on later runs, and ships a Makefile, a golangci-lint configuration, and an envtest-based test setup. Adopting it settles questions a Go project would otherwise decide for itself.

## Decision

The operator is built on the kubebuilder go/v4 scaffold, and the scaffold's layout and generated artifacts are treated as authoritative. Code is written to fit the scaffold; the scaffold is not reshaped to fit a preferred layout.

## Consequences

Easier: `kubebuilder create api` and `create webhook` continue to work, because the paths and anchors they write into are intact. Manifests and deepcopy code stay derived from the types rather than hand-maintained. envtest, Kind-based e2e, and the manager's metrics and leader election arrive already wired.

Harder: the layout is not negotiable. `cmd/`, `api/`, `internal/controller/`, `config/`, and `test/` cannot be renamed or reorganized without desynchronizing the CLI, and that includes `test/utils/`, whose name a general Go convention would reject.

Ruled out: a hand-rolled controller against client-go, and any layout that partitions the module by domain instead of by the scaffold's shape. Upgrading kubebuilder, controller-runtime, or the Kubernetes libraries becomes a coordinated change rather than an independent dependency bump.
