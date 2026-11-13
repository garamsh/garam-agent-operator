# ADR 0086: Restore the Kubernetes operator and keep the gaon operator unreleased

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #431. [ADR 0083](0083-remove-the-kubernetes-manager.md) removed the Kubernetes manager, and [ADR 0084](0084-publish-the-gaon-operator-and-the-guest-image-from-the-release-workflow.md) made `release.yml` publish the gaon operator's image and the guest image beside the control service's, on the ground that goal #416 moved this repository to the gaon operator alone.

That ground is gone. gaon is discontinued (garamsh/aurora#804, owner's decision 1), and its integration in other repositories stays as it is, marked and not removed. The owner's decision 12 there is that the operator goes back to the earlier Kubernetes-based one. Neither ADR 0083 nor ADR 0084 is wrong about what it recorded; the project no longer holds the decision each rests on.

## Decision

1. **The Kubernetes operator is restored as it stood at `bf67ff8`, the commit before #425.** That is the manager, `api/`, `config/`, `test/`, the kubebuilder scaffolding, the Makefile's manifest, generate, install, deploy and Kind targets, `test-e2e.yml`'s Kind suite, and `.golangci.yml` with `logcheck` and `ginkgolinter` as #429 found them. The restoration is a revert of #425, #427 and #429, followed by the edits to the architecture documents below.

2. **`release.yml` publishes what it published at `bf67ff8`:** the manager's image to `garam-agent-operator/manager` and the control service's to `garam-agent-operator/control`. It publishes no gaon operator and no guest image, and its path names neither. The two repositories ADR 0084 named, `garam-agent-operator/operator` and `garam-agent-operator/guest`, are not deleted; nothing pushes to them.

3. **The gaon operator stays in the tree and is not released.** `cmd/gaon-operator`, `cmd/gaon-guest`, `internal/gaon`, `tests/gaon`, `tests/pairrun`, its Dockerfiles, `deploy/charts/garam-agent-operator-gaon` and their checks remain, and `make ci` still runs its tests and the chart check. Its documents say it is discontinued and not released. No feature is added to it, and no gaon integration in another repository is removed.

4. **ADR 0083 and ADR 0084 are superseded by this ADR.** What ADR 0083's seventh decision superseded or amended in other ADRs ends with it, and each status line is what it was before ADR 0083 set it. ADR 0082 and ADR 0085 stay accepted, and their status lines name this ADR for the discontinuation.

## Consequences

- `agent.md` returns, and `configuration.md`, `control.md`, `delivery.md`, `gaon-operator.md`, `integration.md`, `structure.md` and this index describe the restored state. `configuration.md` keeps the control service's and the gaon chart's sections, because the control service and the chart stay.
- `docs/convention/stack-kubebuilder.md` and its row in `docs/convention/README.md` apply again. Restoring them is the PM's, not this change's.
- The control image's ECR repository, the pull role and the `promotion-e2e` environment are unchanged by this ADR. No tag or release is made by it.
- `make test-e2e`, the Kind suite, runs on the promotion again.
- Whether the gaon operator is later deleted is not decided here.
