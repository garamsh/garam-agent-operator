# ADR 0033: Publish the control service as a second image, to a repository of its own, under the rules every image here follows

> Status: accepted; the tags superseded by ADR-0051; one repository per image, for development builds, superseded by ADR-0066
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

`garam`'s service-separation design was accepted on 2026-10-04 as D1 (`garamsh/garam#1155`): this project owns a separately deployed control service with its own desired-state store, and the cluster controller pulls from it outbound. It starts as a second binary in this repository rather than as a repository of its own. The layout half of that is already merged as #204: the manager stays at `cmd/main.go`, governed by `stack-kubebuilder.md`, and a binary that is not the manager follows `stack-go.md` alone (`docs/convention/README.md` §Stack-specific). The control service is to be `cmd/control/main.go`, built from `build/control.Dockerfile`. Neither path exists at this ADR's date; creating them is the control-binary issue's.

That leaves what is published. Two records decide it today, and they are not equally affected:

- [ADR 0014](0014-the-image-repository-is-immutable-and-a-deployment-references-a-digest.md) decides what a tag and a deployed reference are: a repository created immutable and never made mutable, a tag that is the abbreviated hash of the commit built, and a deployment that references the index digest with the tag beside it. None of its reasons is about the manager in particular; each is about any image a deployment pulls and a reader traces to a commit.
- [ADR 0026](0026-single-ecr-repository-dev-tag-by-role.md) decides that "the operator publishes one image, to one ECR repository", and gives that repository two tag roles, `<hash>` and `dev-<hash>`. Its argument is against splitting one image's builds across two repositories by role. It never weighed a second image, because there was none.

The second image cannot share the manager's repository. In an immutable repository whose tag is the commit hash, the manager and the control service built from the same commit would both claim the same tag, and the second push is refused — the refusal ADR 0014 chose. A tag prefix per image would avoid that but would make the tag carry the image's identity as well as its commit, which ADR 0014's tag is defined not to do.

The two images also stay two for a reason about what runs, not about the build. The manager runs in a customer's cluster and holds the cluster controller's credential; the control service runs where this project's operators of the service deploy it, and `garamsh/garam#1155` requires that the hosted service and the controller hold distinct credentials. One image would carry both programs to both places.

## Decision

**This project publishes two images, each to an ECR repository of its own, and every rule ADR 0014 and ADR 0026 set for a tag, a repository and a deployed reference applies to each image unchanged.**

| Image | Entry point | Dockerfile | Repository |
|---|---|---|---|
| Manager | `cmd/main.go` | `Dockerfile` | `garam/garam-agent-operator` |
| Control service | `cmd/control/main.go` | `build/control.Dockerfile` | `garam/garam-agent-operator-control` |

- The manager's image, Dockerfile and repository do not change. The root `Dockerfile` stays the manager's, at the path the kubebuilder scaffold owns.
- Both repositories are in AWS account `486152169996`, region `ap-northeast-2`. The control service's repository name is proposed; creating it is `garamsh/infra`'s.
- Each repository is created immutable and never made mutable. Each image's tag is the 12-hex abbreviated hash of the commit built, with `dev-<hash>` beside it on a bring-up build. Each deployment references its image's index digest with the tag beside it.
- Both images built from one commit carry the same commit tag, in different repositories. The repository says which image; the tag says which commit, as before.

This supersedes ADR 0026's one-image scope — "one image, to one ECR repository" — and nothing else in it. Its tag table, the single repository per image with the role carried by the tag, and the removal of the earlier repositories stand. ADR 0014 is not superseded: its rules were written about a repository and an image, and this ADR applies them to a second of each.

## Consequences

- A reader resolves either image the same way: the repository names the image, the tag names the commit, and `git show <tag>` works for both.
- Publishing one commit is two pushes, one per repository, under the same steps. A commit that changes only one program still publishes under its own hash in whichever repository is pushed; nothing requires both images to be published at every commit.
- `garamsh/infra` declares a second repository. Whether a push role reaches it is that repository's decision, as it is for the first.
- Where the control service's manifests live is the environment repository's, under [ADR 0017](0017-an-unreconciled-environments-values-live-in-an-overlay-here.md); this ADR decides what its image reference must look like, not who carries it.
- The Garam adapter's image stays `garam`'s. This project places it beside an agent and does not build or publish it.

## Rejected alternatives

- **Both images in `garam/garam-agent-operator`, told apart by a tag prefix** (`control-<hash>`). The tag would then carry the image's identity as well as the commit, `git show` would no longer take it directly, and a deployed reference would name the commit only after a reader strips a prefix. It also makes one repository's access policy cover two programs that run in different places.
- **One image carrying both binaries, the entry point chosen at deploy time.** It gives the customer cluster the hosted service's program and the hosted service the controller's, which the separation exists to keep apart.
- **A separate source repository for the control service.** Refused by `garamsh/garam#1155` D1 itself: it adds coordination before a second owner exists.
- **Amending ADR 0026 in place.** Its decision text says one image; changing that is a changed decision, which Rule 2 of `docs/architecture/README.md` sends to a new ADR.
