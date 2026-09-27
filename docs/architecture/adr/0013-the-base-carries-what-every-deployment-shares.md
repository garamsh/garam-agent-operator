# ADR 0013: The base carries what every deployment shares, and an environment's values live where that environment is reconciled

> Status: superseded by ADR-0017
> Date: 2026-08-31

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Measured read-only on `admin@garam-dev` on 2026-08-31. The operator's Deployment ran eight flags this project declares, and `config/` held one of them: `--agent-copy-image`. The other seven — `--garam-address`, `--garam-credential-secret`, `--garam-certificate-file`, `--garam-key-file`, `--garam-trust-file`, `--agent-image`, `--agent-storage-size` — existed in no repository. The four field managers that had written its spec were `kubectl-client-side-apply`, `kubectl-set`, `kubectl-patch` and `kubectl-rollout`: three imperative verbs on top of one apply. `kube-controller-manager` wrote what it owns beside them, and no GitOps controller appeared at all.

One of the typed values was wrong. `--agent-image` named the operator's own image, so every agent's Pod ran the manager binary with no arguments and the manager's own fail-closed guard refused to start — 1361 restarts over six days (#103). A wrong value in `config/manager/manager.yaml` would have been a diff someone read.

The obvious repair is to check all seven in, and it is wrong, because the seven are not alike:

- Four of them are the reading end of something `config/manager/manager.yaml` already writes. The base mounts a Secret by name and mounts it at a path; `--garam-credential-secret` is that name and the three file flags are paths under that `mountPath`. They agreed with the base on the cluster by coincidence of typing, and an owner that held one half would leave the pair spanning two repositories.
- Three of them name something one cluster holds: a service address, an image in one account's registry, a volume size a cluster's storage decides. A base that named any of them would ship one environment's values to every other, and `--agent-image` is the flag that was already wrong for exactly that reason — it named a build.

So the question is not whether to record the configuration but who owns each value, and the answer has to be a test rather than a list, because the next flag has to be assignable without reopening this.

Where the environment's three go is the second question. `config/overlays/<environment>/` in this repository was the alternative considered. `garamsh/gitops` reconciles this cluster and its ADR 0003 already decides the shape — an `Application` under `apps/`, per-cluster values under `clusters/<cluster>/`, a non-Helm resource included through Kustomize (`gitops@8971dd5:docs/architecture/adr/0003-cluster-aware-app-of-apps-with-per-cluster-overlays.md`) — and at `gitops@8971dd5` it held no path named `gagent` or `gagent-bringup`.

## Decision

**A flag whose value is the same wherever this operator runs is the base's. A flag that names something one cluster holds is the deploying overlay's.** That is the whole test, and it assigns the eight: five to `config/manager/manager.yaml`, three to the overlay.

**An environment's overlay is not this repository's to hold.** It lives in the repository that reconciles that environment, which for `admin@garam-dev` is `garamsh/gitops`. This repository states what such an overlay must set and exposes a surface it can set it through; it does not carry one, and no `config/overlays/` is created here.

**What `config/` owes that overlay is a stable patch surface, and a change here may not break it**: `config/default` builds standalone, the manager's Deployment carries one container at index 0, the base sets none of the three so an appended argument is unambiguous, and the manager's image stays the replaceable name `controller:latest`.

## Consequences

A reader answers "where does `--agent-image` come from" from this repository, which is what issue #105 asked for. A flag added to `cmd/main.go` later is assigned by the test rather than by whichever place is nearest, and a value that names one cluster cannot be committed here without failing it.

**This repository cannot finish the job.** The overlay is owed to a repository this project may not write, so the deliverable here is the statement plus the surface, and `--garam-address`, `--agent-image` and `--agent-storage-size` have an owner and no artifact until `garamsh/gitops` carries one. That is deliberate: a fait accompli in someone else's repository was excluded by the issue, and an overlay parked here would be a second unreconciled artifact rather than a home.

Recording where configuration lives does not apply it. The field managers above stay what they are until something reconciles the Deployment, and issue #105 excluded a deploy pipeline; the live object and this repository can still disagree with nothing saying so.

**A decision recorded in `agent.md` moved, and the reason is the base's own volume.** That document said the renewal is unset until a deployment names a Secret, and the base now names one. Re-examined: the Secret the flag names is the Secret the base already mounts, so the name was never the deployment's to choose alone — only to choose twice. What actually keeps a deployment given no identity from renewing is `--garam-address`, which stays the overlay's and gates the whole branch, so a base deployment still starts, still makes no outbound call, and still renews nothing. Under ADR 0008 renewal is not a feature a working deployment declines: expiry is the whole of `garam`'s revocation, so an operator with an address and no renewal is one whose credential stops working on a schedule.

Ruled out: **`config/overlays/<environment>/` in this repository.** Every environment that ever deploys this operator would add a directory to the operator's source, which is the coupling the base-and-overlay split exists to prevent. Ruled out with it: **a values mechanism of this project's own.** `garamsh/gitops` ADR 0003 already holds one, and a second here would be an abstraction built for a caller that has one.

This decision is superseded when an environment needs a value the base cannot express through an appended argument, or when this operator is deployed by something that is not a Kustomize consumer.
