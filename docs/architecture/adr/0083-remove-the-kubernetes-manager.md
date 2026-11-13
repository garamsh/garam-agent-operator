# ADR 0083: Remove the Kubernetes manager and everything only it uses

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- The operator this repository was founded on is a kubebuilder manager: `cmd/main.go`, the `Agent` kind in `api/v1alpha1`, a controller reconciling each `Agent` into a StatefulSet, and the manifests under `config/` that deploy it.
- ADR 0082 imported garam's gaon operator, which runs each agent as a MicroVM from the control service's feed. Goal #416 moves this repository to that operator alone, and the Kubernetes manager is the part it ends.
- The control service, the surface the gaon operator reads, does not depend on the manager. `go list -deps ./cmd` minus the dependencies of `./cmd/control`, `./cmd/gaon-guest`, `./cmd/gaon-operator` and `./cmd/gaon-render` names what only the manager uses.

## Decision

Remove the manager and what only it uses. The set is the output of `go list -deps` above, and nothing else is removed.

1. **Removed code:** `cmd/main.go`, `api/`, `internal/agentname`, `internal/controller`, `internal/desired` (with its credential, placement, renderer and self packages), `internal/garam` (with its constructor), and `test/`, the manager's Kind e2e suite.
2. **Removed deployment files:** `config/`, `PROJECT` and the root `Dockerfile`. This repository holds no deployment manifest after it.
3. **Reduced workflow:** `.github/workflows/test-e2e.yml` stays on its trigger, `promotion-e2e` environment, pull role and pins, and its steps become `make test-e2e-control` and `make pair-run`, with no Kind or manager step. `release.yml` and `make build-images` build and publish the control service's image only.
4. **Removed tooling:** the Makefile's manifest, generate, install, deploy and Kind targets, and the controller-gen, kustomize and setup-envtest tools. `go.mod` drops controller-runtime, client-go, ginkgo and gomega as `go mod tidy` leaves them.
5. **Kept:** the control service and the packages `cmd/control` uses, and the gaon operator's `cmd/gaon-*` and `internal/gaon/`. `tests/control` keeps its control-API coverage through `internal/gaon/agent/controlapi`, the gaon operator's client, and through calls the test makes itself.
6. **`make test-e2e-control` and `make pair-run` stay as local commands, and `test-e2e.yml` runs both on the promotion.**
7. **Superseded ADRs.** The decisions below were about the manager and cease to hold, wholly or in the part each status line names: 0001, 0005, 0006, 0007, 0009, 0012, 0017, 0018, 0023, 0024, 0025, 0027, 0029, 0031, 0034, 0035, 0037, 0042, 0043, 0044, 0045, 0046, 0049, 0054, 0060, 0061, 0064, 0065, 0071 and 0077 wholly; 0015 and 0028 amended in the e2e suite they run; 0030, 0032, 0033, 0057, 0072, 0073 and 0075 in the manager's part; 0082 in the root `Dockerfile` it raised the builder of. ADR 0013, already superseded by ADR 0017, ends with it.

## Consequences

- **The e2e workflow no longer builds a Kind cluster or runs a manager suite.** What it runs on the promotion is the control service's suite and the gaon operator's pair run. The pull role `github-actions-garam-agent-operator-pull`, its `promotion-e2e` environment and the `ecr-pull` policy on `garam/test` already exist and are unchanged.
- **This repository ships no deployment manifest.** Where the control service runs is the environment repository's (`configuration.md`).
- **Dropped test case:** `TestRecovery_TheManagersHalfAgainstGaram` drove the manager's recovery state machine. `TestRecovery_AgainstGaram` covers the controller-side path and the old and new token assertions.
- **Convention passages made moot, not changed here.** `docs/convention/stack-kubebuilder.md` as a whole, its rows in `docs/convention/README.md` §Stack-specific, and the paragraphs on `Dockerfile` and on a Go binary describe code that is gone. `logcheck` and `ginkgolinter` stay in the lint set because that file still mandates them. The PM owns each, and the pull request raises them.
- **The `.devcontainer` and the pull request template are untouched.** They belong to the PM and the conventions.
- **No module is newly required.** `go.mod` only loses requirements.
