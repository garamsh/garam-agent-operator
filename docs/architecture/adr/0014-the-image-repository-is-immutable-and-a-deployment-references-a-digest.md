# ADR 0014: The image repository is immutable and a tag names one commit, and a deployment references the image by digest with the tag beside it

> Status: accepted
> Date: 2026-08-31

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Measured on 2026-08-31, with AWS and `admin@garam-dev` both reachable and read-only. The manager's Deployment ran `486152169996.dkr.ecr.ap-northeast-2.amazonaws.com/garam/gagent-operator-dev:da07a03e1d20@sha256:9764729168e2…` — repository, tag and digest together. The repository held nine image records, which are three pushes of an image index with a `linux/amd64` manifest and a build attestation under each. The three tags were `7792ebe660db`, `f02d007c937e` and `da07a03e1d20`, and each is the abbreviated hash of a commit on this repository's history: `7792ebe` (#70), `f02d007` (#83) and `da07a03` (#86). The digest the Deployment carried was the index's, not the platform manifest's.

None of it was written down here. No workflow logs into a registry; the `Makefile` carries the kubebuilder scaffold's `docker-build`, `docker-push` and `docker-buildx` at `IMG ?= controller:latest` with nothing resolving that default; and `grep -rniE 'ecr|gagent-operator-dev|registry' docs/ README.md` returned no hit about publishing. The only written record was another project's, and it names this repository only to exclude it: `gagent@04ed05a:docs/architecture/adr/0020-immutable-image-repositories.md` says `garam/gagent-operator-dev` "shares the namespace and is the `gagent-operator` project's, not this one's."

So the tag scheme, the reference form, and the publish path are all conventions this project runs in production and never agreed. Whoever pushes next has nothing to conform to, and #101 made the operator a consumer that pulls at every start.

Two facts bound what can be decided:

- **The account is not this project's.** ADR 0020 records what describing another project's account cost: its `deployment.md` described three settings there and one was already false when it was read, and a second project read the description and planned work against it. A description of a setting this repository cannot read goes stale with nothing here able to notice.
- **This project has no build-time check that a tag tells the truth.** `gagent` carries `REVISION` into `org.opencontainers.image.revision` in both its Dockerfiles and refuses a default-`REVISION` build whose context differs from the commit. `Dockerfile` here writes no label and no target compares anything against a commit. A tag in `gagent`'s repositories is a claim a tool declined to let go wrong by default; a tag in this one is a claim a person typed.

The repository's settings were read on the same day and were `IMMUTABLE` with scan-on-push. That is the state on the day, recorded here because an ADR is dated and frozen; it is not the rule, and the rule below does not rest on it.

## Decision

**`garam/gagent-operator-dev` is created immutable and is never made mutable; a tag in it is the abbreviated commit hash of the commit built and never moves; and a deployment references the image by digest, carrying the tag beside it.**

### The digest is the reference

This borrows ADR 0020's argument, and the borrowing is stated rather than assumed: that record binds four repositories and says ours is not among them, so it is evidence here and not authority. The argument is that a tag's stability rests on a repository setting a consumer cannot verify was never changed. It holds here for the same reason it holds there — this image's consumer is the overlay that deploys the operator, which lives in the repository reconciling the cluster (ADR 0013), and it can read the setting today but not the repository's history.

One thing makes the argument stronger here than where it was made. `gagent`'s tag is checked against the tree at build time and this project's is not, so the claim on offer here is the weaker one. An argument for not resting on a tag is not weakened by the tag being worth less.

### The tag is required beside it, and each half has a job

Carrying both is the requirement, not an accident of how the Deployment was typed.

- The **digest** answers which bytes, and is the only half a runtime resolves — a reference carrying both resolves by digest and the tag is not consulted.
- The **tag** answers which source tree, to a person reading the cluster, without an account they may not have. It is a commit's abbreviated hash, so `git show` takes it directly.

Dropping either costs something real. Digest alone makes every deployed reference opaque to a reader who cannot query the account, which is this issue's own complaint moved from the repository into the cluster. Tag alone rests the identity of the running bytes on a registry setting nobody here can audit.

### The digest is the index's

A push produces an image index and the digest a deployment carries is that index's, so a node resolves the platform manifest under it. `stack-container.md` decides the same question for a base image a `Dockerfile` names — take the index's digest, not one platform's — and a deployed reference is that choice one layer out: a manifest digest pins the architecture too, silently, and this project states no rule about which architectures it publishes.

### Immutable, and the tag names one commit

The two are one decision: a tag cannot move in a repository that refuses the second push of a name. It is stated as immutability because that is the property whoever creates the repository has to act on, and because the setting is retroactive in effect — it belongs to the repository rather than to a tag, and no tag records the policy it was pushed under, so a repository that is ever mutable leaves every tag it holds a readable label rather than an identifier, including the ones pushed while it was immutable.

Since nothing here checks that a tag names the tree it was built from, immutability is the only property keeping a tag a claim about one commit at all. It bounds the failure to a first push: a mislabelled image can be pushed once under a free name and never over one already taken.

## Consequences

- **A reader of this repository can answer where the running image came from.** That is what issue #102 asked for, and `delivery.md` is where the answer is kept current.
- **Republishing a commit is refused rather than overwritten.** The repair for a bad image is a new commit, not a re-push. That cost is accepted: it is the same refusal this is chosen for.
- **The tag stays an unchecked claim.** Deciding the form does not add the guard `gagent` has, and this does not add one — that is a change to `Dockerfile` and the `Makefile`, which #102 excludes. What bounds the damage is immutability and the fact that the digest, not the tag, is what runs.
- **Nothing here enforces any of it.** The obligations bind whoever creates the repository and whoever pushes to it. This repository cannot read the setting and does not try.
- **The account's own hygiene is unconstrained by this.** It states what this project requires, not what the account may hold, so a lifecycle or scanning policy added there for reasons of its own contradicts nothing and needs no change here.
- **No release repository is decided.** Only the bring-up repository exists, and this binds that one. What a release would be tagged, and whether a second repository is created, is left open because no release path exists to decide it for.

## Rejected alternatives

- **A moving tag — `latest`, or a `dev` that always names the newest build.** It cannot exist in an immutable repository, and naming a commit is the tag's whole job here. `garam` uses moving tags for other applications' bring-up repositories, so a reader comparing them should see a deliberate difference.
- **Digest only, with the tag dropped.** It costs nothing to a runtime and everything to a person: the deployed reference stops naming a commit, and the reader who has no account access — the reader this issue exists for — is left with a hash that resolves nowhere they can look.
- **Tag only, as `gagent`'s own tags would allow.** Refused on the ground ADR 0020 gives and on one of this project's own: there is no build-time check here, so the tag is a weaker claim than the one that argument was already refused for.
- **A platform manifest's digest.** It pins the architecture as a side effect of pinning the bytes, and nothing records that it did. `stack-container.md` refuses the same shape for a base image.
- **Making a description of the account true, and keeping it.** That is the form ADR 0020 removed from `deployment.md` after one of three described settings turned out to have been false when read. The false clause was the visible instance; the form was the defect.
- **Requiring a lifecycle policy or scan-on-push.** Refused rather than inherited: this project depends on neither, and a requirement stated without a dependency behind it is a description of the account wearing a rule's clothes. Whether the account runs either is `garamsh/infra`'s.
- **Building a publish pipeline, so the convention is enforced rather than written.** Excluded by #102, which pushes no image, and it is a decision with its own alternatives — which credential, which trigger, whether CI may write that account. Recording what a human does is what is deliverable here, and an honest manual path beats silence.
