# ADR 0003: Govern Go with one stack file written for the operator

> Status: superseded by ADR-0004
> Date: 2026-08-16

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The conventions template shipped `stack-go.md`, written for a general Go service. Five of its rules contradict the scaffold ADR 0001 adopted:

- Its directory layout is `internal/<domain>/service.go` with per-verb files. The scaffold uses `api/<version>/` and `internal/controller/`, and has no place for `service.go`.
- It bans `utils/` at any level. The scaffold writes `test/utils/`.
- It mandates `log/slog`. controller-runtime logs through `logr`, and `cmd/main.go` installs a zap-backed implementation.
- It mandates testify and mockery. The scaffold uses Ginkgo v2 and Gomega, and `.golangci.yml` enables `ginkgolinter`.
- It places end-to-end tests in `tests/` at the module root. The scaffold uses `test/e2e/`.

Left in place, every one of these would be violated by the first controller merged, and the reviewer would be choosing case by case which rule to waive.

Two shapes were considered. Keep `stack-go.md` for general Go rules and add an operator file for the scaffold-specific ones; or replace it with a single file covering both. The conventions index forbids two files governing one artifact — both would govern the same `.go` files, and naming, error handling, and import rules do not divide cleanly from layout, logging, and testing.

## Decision

This repository keeps one stack file, `stack-kubebuilder.md`, which owns every Go rule here including the general ones. `stack-go.md` is deleted from this repository and remains in the conventions template for projects that are not operators.

## Consequences

Easier: a contributor opens one file to find every rule that governs a `.go` file, and the layout, logging, and test rules there match what the scaffold actually produces. The one-file-one-territory rule holds without a negotiated split.

Harder: the general Go rules now live in two places across the organization — this file and the template's `stack-go.md` — and an improvement to one does not reach the other. `stack-kubebuilder.md` is longer than either would have been alone.

Ruled out: layering two stack files over the same source tree, and waiving `stack-go.md` case by case in review.
