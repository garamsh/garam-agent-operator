# ADR 0082: Import garam's gaon operator and keep it in this repository

> Status: accepted; the gaon operator discontinued and not released by ADR-0086
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #418. The operator that runs each managed agent as one MicroVM on a gaon cluster was built in `garamsh/garam`, as the module `gaon-operator`, and is to be maintained here, beside the control service whose feed it reads and the manager it registers with.

- The source is garam commit `568c6e61e57e41ee9ebf66ac9742b5ee3fa6fd21`. Its own checks were its unit tests and a pair run of 54 checks against garam's services built in garam's tree.
- This repository is one Go module, `github.com/garamsh/garam-agent-operator`. garam's `lib/errorx`, `lib/grn` and `lib/platform-sdk-go` are modules of garam's repository that the operator requires, and gaon's `lib/api` is a module of `garamsh/gaon`.
- garam's pair run built garam's services from garam's tree. This repository has no such tree. It does have `make garam-e2e`, which copies the `garam`, `testprincipal` and migrations files out of garam's published test image, whose digest is pinned in `Makefile`.
- The code's comments and the operator's document cite garam's ADR-0151, ADR-0153, ADR-0154, ADR-0156 and ADR-0015, and `platform/api/machine.yaml`. Those are garam's, as they stand at `568c6e6`, and this repository's ADRs of the same numbers are other decisions.

## Decision

Import the operator into this repository as it stood at `568c6e6`, as part of the one module, and keep it here. The decisions that follow were left open by the issue.

1. **One module, `go 1.27.4`.** The operator's `go.mod` and `go.sum` are merged into the root ones, and the go directive rises to the operator's. The builder images of `Dockerfile` and `build/control.Dockerfile` rise to `mirror.gcr.io/library/golang:1.27.4-alpine3.24` by digest, because the older builder cannot build a module that declares the newer directive. The registry and the alpine flavour changed with the version, because `build/operator.Dockerfile` and `build/guest.Dockerfile`, imported from the source, build with that one digest, so one builder image serves all four builds and no flavour is chosen twice. The existing code compiles and passes its tests unchanged under it. `golangci-lint` rises to v2.15.0 in `Makefile` and `.custom-gcl.yml`, because the version before it could not read export data written by the newer toolchain.
2. **The garam libraries are required at garam commit `43d700d30910319095b7f97230bffc79c2328981`,** as the pseudo-version `v0.0.0-20261113015820-43d700d30910` of each of `lib/errorx`, `lib/grn` and `lib/platform-sdk-go`. That is the commit of the test image the pair run uses, an ancestor of `568c6e6`, and only one commit between the two touched `lib/` or `platform/`, a change to `go.mk`, so the three libraries are the same at both.
3. **gaon's `lib/api` stays at the source's pin,** `v0.0.0-20261113042354-2387cebd0b44`, commit `2387cebd0b44b322b7da2ff485144a7456a97c6f` of `garamsh/gaon`. The issue did not name it. The source's pin is the version the operator's tests and the 86-check gaon-mode pair run were written against.
4. **The code is copied to `internal/gaon/`, `cmd/gaon-*`, `tests/gaon/` and `tests/pairrun/`.** Its internal imports are rewritten to the new paths. Its other text is kept, including the comments that cite garam's records, so that the diff against the source stays what the move and the checks required. The few lines changed for this repository's linter are listed in the pull request.
5. **The pair run is redesigned around garam's test image.** `make pair-run` runs garam's authorization engine, API and machine listener as host processes from the files `make garam-e2e` copies, over one PostgreSQL container, with the test-principal fixture in place of a sign-in. The control service is built from `cmd/control` in this tree, not taken from a pinned image. gaon is the stand-in of `tests/gaon/gaonstandin`. `make pair-run-gaon` runs the same steps against gaon's own services, built from a checkout `GAON_REPO` names; it is not part of any check and was not run for this decision.
6. **Two images build from `make operator-image` and `make guest-image`, and nothing pushes them.** They are not in `make build-images`: the guest image's three sources are pinned by digest in `build/guest.Dockerfile` and live in a private registry, so building it needs a login. No workflow publishes either, and which repositories and tags they take is left to the decision that adds one.
7. **The gaon-mode harness is a Go file this module compiles and lints, at `tests/pairrun/gaonharness/main.go`.** It sits outside `testdata`, which Go skips, so `lint-coverage` reaches it, and behind the build tag `gaonharness`, which `.golangci.yml` names beside `e2e`, so `go build ./...` does not compile it. It imports gaon's embedded etcd and mvmd's runtime API, which the root `go.mod` therefore requires: `go.etcd.io/etcd/server/v3` at v3.6.14, with its sibling etcd modules raised to the same release (this repository required no etcd module before this decision), and `github.com/garamsh/mvmd/lib/runtimeapi` at `v0.0.0-20261111091810-3b37892f303a`. Requiring mvmd raised grpc, protobuf, genproto and `golang.org/x/*` in the same file. `make pair-run-gaon` builds the file inside a gaon clone, with gaon's own versions, so these requirements serve the lint and not the build of the harness. v3.6.14 is the lowest 3.6 release that fixes the five advisories published against the 3.6 line that were open at v3.6.8 (GHSA-q8m4-xhhv-38mg and GHSA-rfx7-8w68-q57q, fixed in 3.6.9; GHSA-x35m-3gp4-4fh5, fixed in 3.6.11; GHSA-6vch-q96h-7gc3 and GHSA-xg4h-6gfc-h4m8, fixed in 3.6.14). GitHub's advisory database lists no advisory covering v3.6.14 or any other module this decision newly requires or raises.

## Consequences

- One commit can change the control service's feed and the operator that reads it, and `make ci` compiles, lints and tests both.
- The module now requires three packages of a repository this one does not own, at a commit chosen by the test image. A new test image is a new commit to require; the two move together, in one pull request.
- Only the guest image's three source images, in a private ECR repository, need a login to build. `lib/api` of gaon is a public module and resolves without one.
- The pair run proves the operator against garam's real services and this repository's control service, and against a stand-in for gaon. Against gaon itself it is proved only by `make pair-run-gaon`, which no check runs.
- A reader of the operator's comments meets ADR numbers that are garam's. The operator's document says so at its head.
- This rules out, until another decision changes it, a `replace` directive to a folder and any reference from this tree to a path of garam's repository.
