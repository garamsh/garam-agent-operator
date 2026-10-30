# ADR 0079: Run only on our own system, and keep the seams that allow a bring-your-own operator

> Status: accepted
> Date: 2026-10-30

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #403. The owner decided on 2026-10-30, recorded at `garamsh/garam#1655`, comment 6524863992:

- **Item 1.** `garam-agent-operator` runs only on our own system. It is a project for our platform, not a product customers install.
- **Item 2.** BYO operator is on hold. This changes decision 2 of that issue's proposal, which kept BYO as a requirement. Code stays written so the project can switch to BYO at any time, so nothing removes the seams that keep it possible.

Items 3 and 4 of that comment concern `gaon`, `mvmd` and `garam`'s repository. They are not this decision.

Three frozen records state customer deployment:

- **[ADR 0032](0032-own-an-agents-desired-definition-in-this-projects-control-service.md)** places the manager "in a customer cluster", and rule 4 says every connection leaves it.
- **[ADR 0033](0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md)** gives that placement as a reason for two images.
- **[ADR 0036](0036-persist-the-control-services-desired-state-in-its-own-postgresql-database.md)** rejects Kubernetes objects as the store because the hosted service "is not in the customer's cluster".

What the deployment and the sources say, read for this decision:

- **The two binaries do not run in different places today.** At `garamsh/aurora@8aada7c`, `kubernetes/applications/garam-agent-operator.yaml:111` points the manager at `garam-agent-operator-control.garam-agent-operator-system.svc.cluster.local:8080`. Both `Application`s deploy to `https://kubernetes.default.svc`, namespace `garam-agent-operator-system`. So place cannot be the reason for two images.
- **The two credentials are distinct there.** The manager's is the Secret `garam-credential` (same file, line 14). The control service's is `garam-agent-operator-control-credential` (`kubernetes/components/garam-agent-operator-control/control.yaml:115`).
- **`garamsh/garam#1155` §2 still requires that.** It reads "Hosted API and cluster-controller credentials are distinct; migration must not copy a controller private key into the hosted service." The sentence names neither a customer nor a place, so item 1 does not reach it. The body and its acceptance were unchanged when read on 2026-10-30.
- **The same section's "Outbound mTLS" is half current.** [ADR 0072](0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md) replaced the client certificate with a token. The direction stands.

## Decision

1. **This project runs only on our own system.** It is not a product customers install, and no document here states customer deployment as today's fact.

2. **BYO operator is on hold, and the code stays able to switch to it.** Nothing here builds toward BYO, and nothing removes what would allow it.

3. **These seams are kept.** Each is what would have to stay separate for a customer to run the manager. Paths and lines are as read at `063a455`.

   | Seam | Where it is | Removed by |
   |---|---|---|
   | Two binaries, with no domain in common | `cmd/main.go:28-36` and `cmd/control/main.go:24-36`. `go list -deps` of the two shares `internal/machinetoken` and `internal/secretref` and no other package of this module. | The manager importing `internal/definition`, `internal/console`, `internal/distribution` or `internal/execution`, or the reverse with `internal/controller` or `internal/desired` |
   | Two images, one repository each | `Dockerfile:27,45`, `build/control.Dockerfile:18,30`, `.github/workflows/release.yml:110` | One image carrying both binaries |
   | Distinct credentials | The manager's key files, `cmd/main.go:105-111`. The control service's, `cmd/control/main.go:93-98`. The manager's token names the service as its audience, `cmd/main.go:128,375-376`. | Either binary reading the other's key, or one identity for both |
   | Connections are outbound, with no broker | The manager dials the control service, `internal/desired/client.go:156-157`, and `garam`, `internal/garam/client.go:91`. An agent's adapter dials the service, `internal/controller/agent_statefulset.go:914`. The service listens at `cmd/control/main.go:201-202`, and its only HTTP clients are to `garam`, `cmd/control/main.go:264,270`. Its flags, `cmd/control/main.go:85-98`, name no address of a manager. | Anything dialling into the manager's cluster, or a relay through `garam` |
   | One store, outside the manager | Only the control service opens it, `cmd/control/main.go:135`. The manager has no `pgx` among its dependencies and receives state over the wire, `cmd/main.go:375`. | The manager reading the store, or the store moving into cluster objects |
   | The control service holds no cluster access | `go list -deps ./cmd/control` carries neither `k8s.io/client-go` nor controller-runtime. `cmd/control/main.go:24-36` wires no Kubernetes client. | The service reading or writing a cluster |
   | Addresses and trust are the deployment's | `cmd/main.go:102,114,121,125,128`, `cmd/control/main.go:90-91`. No non-test Go source or manifest under `cmd`, `internal`, `api` or `config` names a `cluster.local` address. | Code assuming the two share a cluster, namespace or trust root |
   | A controller is served only its own agents | The feed is keyed by the proved controller, `internal/distribution/desired.go:105`, and the routes are `/v1/operators/self/...`, `internal/distribution/server.go:51-55`. | A controller reading another's agents, or a feed shared by all |
   | Provisioning stays outside the manager | `config/default/kustomization.yaml:20` builds the manager with nothing of the control service. Its key arrives as files a person places, `cmd/main.go:105-111`. Agents' images are its flags, `cmd/main.go:131,135,140,145`. | The manager's install needing the service's manifests, or an identity it provisions for itself |

4. **A change removing a listed seam is a change to this decision, not a simplification.** It needs a new ADR superseding this one, whatever it saves. Item 2 of the owner's decision is what stands behind a seam that looks unused today.

5. **The image split rests on distinct programs and credentials, and on this decision.** It does not rest on where the binaries run. [ADR 0033](0033-publish-the-control-service-as-a-second-image-to-its-own-repository.md)'s other reasons stand.

What this amends, each body staying as written:

| ADR | Amended | Stands |
|---|---|---|
| 0032 | The customer's cluster as where the manager runs | The ownership split and all five rules. Rule 4 now reads as the manager's cluster, and it is a seam above. |
| 0033 | The customer's cluster as a reason for two images | The two images, and the requirement of distinct credentials |
| 0036 | The customer's cluster as the ground for rejecting Kubernetes objects | The rejection. The store stays out of cluster objects because the seams above keep it there, and `garamsh/garam#1155` D2's other grounds stand. |

## Consequences

- **A seam can look like needless cost.** With both binaries in one namespace, two images, two identities and a TLS connection between neighbours protect nothing the deployment needs today. Decision 4 is what answers a proposal to fold them.
- **The seams are not proof that BYO works.** The deployment read above crosses no cluster boundary between the manager and the control service, so that connection has not been measured across one by it.
- **The list is a reading, not a guard.** No check fails when a seam goes. A reviewer holds it, against this table.
- **Nothing changes in code, image, credential or connection direction.** This record changes what the documents state.

## Rejected alternatives

- **Restate the image split as "the two binaries run in different places".** It does not hold at `garamsh/aurora@8aada7c`, where they share a namespace.
- **Drop the seams while BYO is on hold.** Item 2 of the owner's decision refuses it.
- **Edit the customer wording in the three ADRs.** Their bodies are frozen, and the status lines and this table carry the change.
- **Let `control.md` alone carry this.** The list is a decision a later change has to answer to, so it needs a record that does not move.
