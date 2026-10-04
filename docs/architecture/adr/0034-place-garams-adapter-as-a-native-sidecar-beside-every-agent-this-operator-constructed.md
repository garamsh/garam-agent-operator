# ADR 0034: Place garam's adapter as a native sidecar beside every agent this operator constructed, and own its placement only

> Status: accepted
> Date: 2026-10-04

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #216. `garam`'s adapter is the process that carries messages between `garam` and one agent: it claims a message, posts it into the agent's gateway, and completes the claim. Before this change nothing in the Pod this operator builds ran one, so `garam` could deliver no message to any agent here. `agent.md` recorded who builds it as an open question.

Ownership was settled with `garam`'s PM on #216. **`garam` owns the `garam adapter` command**: its delivery loop, stable message identifiers and completion, outbox forwarding, activation, and runtime-status reporting (garamsh/garam#1169). **This operator owns placement**: the Pod the adapter runs in, the image reference, the adapter's configuration, and mounting what it reads.

**Every claim about `garam` below was read at `garam@fdfb76d`.**

- **The adapter is a subcommand of `garam`'s one image.** `deploy/local/Dockerfile:58` declares `ENTRYPOINT ["garam"]` with no `CMD`, and `adapter` is the subcommand (`internal/cli/delivery.go`).
- **It has to run in the agent's Pod and outlive the agent.** The gateway binds loopback, and the adapter has to outlive the agent process during shutdown, so it is a native sidecar rather than an ordinary container (`docs/architecture/adapter.md:28-33`).
- **It answers no probe.** It has no listener (`docs/architecture/deployment-contract.md:473-477`).
- **It reads seven settings, all from the environment** (`internal/cli/cli.go:64-81,172-178`):
  - `GARAM_ADAPTER_AGENT`, the agent's GRN, required;
  - `GARAM_ADAPTER_MACHINE_URL`, required;
  - `GARAM_ADAPTER_GATEWAY_URL`, which defaults to `http://127.0.0.1:8080`;
  - `GARAM_ADAPTER_GATEWAY_AGENT`, the ID the gateway serves the agent under, required;
  - three file paths, all required: the agent's certificate and key, and the `garam` server root.
  - `internal/cli/delivery.go:80-92` and `:140` refuse to start without the required ones.
- **The key file follows `garam`'s rule:** owned by the reading process's uid, with no group or other bit, re-read at every handshake (`docs/architecture/deployment-contract.md`).

On the `sherlock` side: the gateway listens on `addr`, whose default is `127.0.0.1:8080` (`sherlock@ecf4621:internal/config/config.go:25,167`). It serves the agent under the `--agent-id` it was started with, which is the GRN ([ADR 0037](0037-carry-an-agents-identity-in-its-spec-and-start-the-agent-under-it.md)). `sherlock` writes its durable outbox to `outbox/` beside its memory file (`sherlock@ecf4621:internal/gateway/outbox.go:18-26`). In the Pod this operator builds, that is `/var/lib/sherlock/memory/outbox` on the state volume. The adapter at `garam@fdfb76d` reads no outbox: no setting names one, and `adapter.md` §Stable identifiers and the outbox says "Nothing in this section is built."

## Decision

**The adapter runs as a native sidecar of the agent's Pod**: an init container named `adapter` with `restartPolicy: Always`, placed after the init containers that run to completion. So it starts after the credential is copied and keeps running beside the agent.

- **Image:** the one the new manager flag `--agent-adapter-image` names, pulled at every start, as every container of the Pod is.
- **Command:** `args` is `adapter`, and `command` is left to the image.
- **Probes:** none.
- **Security context:** every container's.
- **Source of the image:** never a definition or template field, for the reason [ADR 0007](0007-claim-definitions-from-a-poller.md) gives `--agent-image`: a console user would be choosing what this operator runs in a cluster they cannot see.

**It is built only where it can start:**

- an adapter image is named;
- `--garam-address` is set, which becomes `GARAM_ADAPTER_MACHINE_URL` as `https://<address>`, the same listener this operator reaches;
- the `Agent` carries `spec.identity`, whose GRN `garam` requires.

A hand-written `Agent` has no GRN and `garam` does not know it (ADR 0031 §4), so it gets no adapter. Where any condition fails, the Pod carries no adapter and nothing else changes. One that stops meeting them has the sidecar taken back out, as the workspace is when its image is unset ([ADR 0023](0023-run-an-agents-workspace-as-a-second-container-this-operator-names.md)).

**Both ends of the gateway link are written by this operator.** The adapter is told `GARAM_ADAPTER_GATEWAY_URL=http://127.0.0.1:8080`. The agent is told `SHERLOCK_ADDR=127.0.0.1:8080` where the adapter is built. Two defaults that agree are not one value chosen, which is ADR 0023's ground for the workspace's address. The address is a new field of the type's descriptor ([ADR 0029](0029-route-every-agent-specific-name-in-the-pod-through-its-types-descriptor.md)), because it is the agent binary's. `GARAM_ADAPTER_AGENT` and `GARAM_ADAPTER_GATEWAY_AGENT` are both the GRN.

**The adapter mounts the agent's credential copy read-only, and nothing else.**

- It mounts the copy ADR 0010 delivers, at `/run/garam/credentials`, and its three file settings name `certificate.pem`, `key.pem` and `server-root.pem` there, the keys the constructor places.
- It is the agent to `garam`, so it reads the same key file, and it owns it because every container runs as the Pod's user.
- It mounts neither the state volume nor the config directory, and the agent container gains no mount for it.
- The adapter's paths are the same for every agent type, so they are this operator's constants and not the descriptor's.

**Two mounts are named and not built, at one seam beside the credential's:**

- **The placement token Secret from #212**, which the adapter alone will read.
- **The agent's outbox.** It is not mounted because the adapter has no setting naming where to read it, and a mount it cannot be told about is one nothing reads (`simplicity.md` §Rules, no speculative abstraction). Its ownership is also undecided. The adapter owns reclamation, so it needs write access. A `subPath` mount of `memory/outbox` that the kubelet creates before `sherlock` does would be root-owned, and `sherlock` could not write it. That is decided once the adapter's need is a setting rather than an assumption.
- garam#1169's consumer side adds the outbox mount together with the setting that names it.

## Consequences

- **`garam` can deliver messages to an agent this operator constructed**, once `--agent-adapter-image` is set. Unset, every Pod is the one built before, and the manager says so once at startup.
- **This operator implements no delivery behaviour.** What the adapter does with a message, its outbox and its status are `garam`'s, and a change there reaches the Pod by naming another build.
- **The adapter's resource requests are unset.** `garam` publishes no figure for them (`deployment-contract.md` §Resource requests and limits), and this operator invents none.
- **The `restricted` standard still admits the Pod.** The sidecar carries the same security context as every other container. The integration layer creates the Pod in a namespace enforcing `restricted`, alongside a control the standard refuses.
- **Nothing here has run the real adapter.** envtest has no kubelet, and the e2e suite's stand-in agent and its Pod carry no adapter, because the suite constructs no agent. Whether the adapter claims and delivers is the joint acceptance #216 leaves out of scope.
- **The adapter's settings are `garam`'s names at one commit.** A rename there is an adapter that refuses to start, and the citation above is what a reader checks first.

Ruled out:

- **An ordinary container.** It is not guaranteed to outlive the agent during shutdown, and `garam` names the native sidecar as the shape.
- **The image as a definition or template field.** That is ADR 0007's reason against letting a console user choose a container.
- **A probe.** The adapter listens on nothing, and `garam` decides none for it.
- **Mounting the state volume for the outbox now.** It would hand the adapter the agent's memory store too, and nothing would read the mount until garam#1169 lands.
- **Building the adapter beside a hand-written `Agent`.** `garam` refuses to start without a GRN, so the sidecar would restart forever.
