# gaon-operator

The operator that runs each managed agent as one MicroVM on a gaon
cluster. It was built in garamsh/garam and is imported here at garam
commit `568c6e61e57e41ee9ebf66ac9742b5ee3fa6fd21`
([ADR 0082](adr/0082-import-garams-gaon-operator-and-keep-it-in-this-repository.md)).
An ADR number written `garam ADR-NNNN` below, and the issue numbers
written `garam#NNNN`, are garam's, as they stand at that commit; the
operator's decisions are in garam's garam ADR-0151, garam ADR-0153, garam ADR-0154 and
garam ADR-0156 there. Where this document says `ADR 0042`, it means this
repository's.

**Built: the reconciler (garam#1834), garam's and the control service's
wire clients, the binary that composes them (garam#1839), the guest
supervisor and composed guest image (garam#1846), the MicroVM's
command, environment and placement token (garam#1848), and the
placement's registration and the agent's configuration and model key
(garam#1853, garam ADR-0154).** `cmd/gaon-operator` exchanges its
assertion with a key garam issued, reads the control service's feed as
its registered controller identity and reconciles into gaon. `make operator-image`
and `make guest-image` build the operator's image and the guest's
without pushing either. `release.yml` publishes them as
`garam-agent-operator/operator` and `garam-agent-operator/guest`
(ADR 0084), and the deployment is not built (§What is not built).

## What is built

`internal/gaon/agent` holds the reconciler. `Service.Reconcile` is one
level-triggered pass: it reads the whole feed, lists the objects it
labelled in gaon, and writes what differs. What a restart loses is only
which placement bodies it has presented, and it presents them again.

- **Objects.** An agent yields one `Secret`, two `Volume`s (memory and
  workspace) and one `MicroVM`, named `agent-` plus 16 hex digits of a
  SHA-256 of the agent's GRN (`-memory`, `-workspace` on the Volumes).
  Labels are `garam.sh/role=agent` and 32-hex digests of the
  organization and agent; the GRN, epoch and revision are annotations,
  because a label value cannot hold a GRN.
- **No credential in a spec.** The tokens, the placement token and the
  two trust roots live in the `Secret`. The MicroVM's files take them
  with `valueFrom.secretKeyRef`, mode 0400, owner 65532 (the runtime's
  and adapter's account; the MicroVM's `user` is `0:0`); no file
  carries inline content that is secret, and no `env` entry does
  either. The two inline files are the runtime's configuration and ego,
  which hold none.
- **Command and environment.** `command` is
  `["/usr/local/bin/gaon-guest"]` and `args` is empty: gaon's
  `command` replaces the image's entrypoint, and the supervisor builds
  each program's arguments itself. `env` holds `GUEST_AGENT_GRN` and
  `GUEST_ASSIGNMENT_EPOCH` from the feed, `GUEST_MACHINE_URL` and
  `GUEST_CONTROL_URL` from the settings, `GUEST_RUNTIME_CONFIG` and
  `GUEST_RUNTIME_EGO` naming the two files below, and
  `GUEST_RUNTIME_ENV` naming the model key's variables, each a
  `valueFrom.secretKeyRef`.
- **The runtime's configuration** (`internal/gaon/agent/runtime.go`). From
  the feed's `configuration` the operator renders
  `/run/garam/runtime-config.yaml` (`model` with its provider, endpoint,
  name and `api-key-env`, `embedding`, `tools.pins`) and
  `/run/garam/runtime-ego.md` as `files` entries with inline `content`,
  mode 0400, owner 65532. Inline, not a `ConfigMap`: they hold no
  secret, 65532 alone reads them so 10001 cannot read the pins, and no
  object per agent is added. gaon bounds inline files at 64 and 1 MiB in
  all. The key is an `env` entry, `MODEL_API_KEY` (and
  `EMBEDDING_API_KEY` where an embedding names one), with
  `valueFrom.secretKeyRef` of the Secret and key `apiKeyRef` names as
  `<secret-name>/<key>`; the operator never reads the value. The
  Secret must exist and carry `garam.sh/role=model-key` and
  `garam.sh/organization=<digest>` for the agent's own organization;
  an `apiKeyRef` naming any other (absent, unlabelled, another
  organization's, one of the operator's own `agent-<16 hex>` Secrets),
  or a configuration that cannot be rendered, writes nothing for the
  agent and fails the pass with an error naming the reference.
  `profile.storageClassName` and
  `profile.resources` are parsed and not used: the Volume has the one
  class `api.StorageClassLocal`, and `Options.Resources` sets the
  MicroVM's. The instructions file and message issuers are not
  rendered.
- **Placement.** Once the MicroVM exists, `register` presents
  `{epoch, podUid, pvcUid, tokenSha256, previous: null}` to the control
  service: the MicroVM's and the memory Volume's `metadata.uid`, and the
  SHA-256 of the placement token read from the agent's Secret.
  `previous` is not built. 201 and 200 are both success; an identical
  body is not presented again. A 409 `placement_superseded` or
  `epoch_superseded` is never presented again; another 4xx is presented
  again after five minutes; a 5xx or transport error fails the pass. A
  stopped agent is not registered, and the state is in memory, so a
  restart presents the same body and reads the repeat as a repeat.
- **Volumes are made once.** They are created only where the agent has
  no MicroVM yet. Where the MicroVM exists and a Volume is missing, the
  operator creates nothing for that agent, returns an error naming the
  Volume and reports no state: a Volume is removed only by a person
  (garam ADR-0151 §3).
- **Mint once.** A Secret that exists is never minted over. One that is
  missing is minted again, which is what makes a restart lose nothing.
  The placement token is minted with it: 32 bytes from `crypto/rand`,
  hex-encoded, under the key `placement-token`, as
  ADR 0042 mints it. A Secret that exists keeps
  what it holds, so one made before this change is not given a token.
- **Report.** `observe` maps a MicroVM to `provisioned`, `ready` or
  `failed` by the ADR's table, reading the phase through
  `EffectivePhase` so a status of another `bindingEpoch` counts as
  pending. `failed` needs a last exit. Nothing is reported where nothing
  was read, and each change is reported once. A stale epoch refused by
  garam is dropped and logged.
- **Halt, never delete.** A `stopped` agent, and one that left the feed,
  has its MicroVM's `runStrategy` set to `Halted`. Nothing is deleted:
  the Volumes are the agent's memory.
- **Settings.** `internal/gaon/config` reads addresses and file paths from
  the environment and names each missing variable, never a value.

gaon's side is `lib/api` at `github.com/garamsh/gaon` commit
`2387cebd0b44b322b7da2ff485144a7456a97c6f`, the commit `make pair-run-gaon`
builds, and the version `go.mod` requires. garam's side is `lib/errorx`,
`lib/grn` and `lib/platform-sdk-go`, required at the pseudo-version of garam
commit `43d700d30910319095b7f97230bffc79c2328981`, the commit of the test
image the pair run uses (ADR 0082).

### The wire clients and the binary

- **Exchange** (`internal/gaon/token`). A `Source` signs a `private_key_jwt`
  (RS256, `typ` JWT, `kid` the key's RFC 7638 thumbprint, `iss` and
  `sub` the client id, `aud` the token URL byte for byte, `iat` and
  `exp` 60 seconds apart, a fresh `jti`), exchanges it at garam's token
  endpoint and holds the token in memory until a minute before it
  lapses. The key is read again at each exchange. Two Sources exist:
  garam's own, and one that asks for `audience` the control service's
  GRN, which only the control service accepts.
- **Garam** (`internal/gaon/agent/machineapi`). `MintTokens` mints two
  tokens under contract `machine-token.v1`: the agent's own and the
  control token, minted with the control service as audience. A token
  minted at an epoch other than the feed's, and a 409
  `failed_precondition` from `reportProvisioningState`, are
  `agent.ErrStaleEpoch`. A 401 drops the held token and asks once more.
- **Feed** (`internal/gaon/agent/controlapi`). `GET
  /v1/operators/self/desired?after=&waitSeconds=` under the control
  token and `Controller-Grn`; the status report is `POST
  /v1/operators/self/agents/{grn}/status`. The cursor moves only when
  the loop settles it after a pass that succeeded, so a failed pass
  asks again at once. A profile without `workspaceStorageSize` gives
  the workspace Volume the size of the memory Volume.
- **An answer that cannot be read whole is an error, never an empty
  set**, because an empty set halts every agent. These all fail the
  pass: a status other than 200; a body cut short or longer than 16 MiB;
  invalid JSON, trailing data, an unknown field, a missing cursor, a
  null or absent `agents`; an agent without a required field, with a
  GRN that is not a canonical agent GRN, with a `revision` or `epoch`
  that is not a canonical decimal string, with a bad quantity, or named
  twice. Only `{"cursor": "...", "agents": []}` is an empty set.
- **Loop and health** (`internal/gaon/loop`, `internal/gaon/health`). Passes
  follow one another, the long poll pacing them; a failed pass is
  retried after 1 second doubling to 1 minute. `/healthz` is 200 while
  the process runs and `/readyz` while the last pass succeeded. SIGINT
  and SIGTERM stop the loop and the listener.

### Settings

Addresses and paths, in the environment; none holds a credential.

| Variable | Meaning |
|---|---|
| `GAON_API_URL`, `GAON_CA_FILE`, `GAON_CLIENT_CERT_FILE`, `GAON_CLIENT_KEY_FILE` | gaon's API, its CA, and the operator's client certificate and key |
| `GARAM_MACHINE_URL`, `GARAM_ROOT_FILE` | garam's machine listener and its root |
| `GARAM_TOKEN_URL` | the token URL the key issuance answered, the assertion's `aud` |
| `CONTROL_URL`, `CONTROL_ROOT_FILE`, `CONTROL_GRN` | the control service, its root, and its operator GRN |
| `OPERATOR_KEY_FILE`, `OPERATOR_CLIENT_ID` | the operator's key, and the controller's operator GRN, which is also the `Controller-Grn` |
| `AGENT_IMAGE` | the composed guest image, as `<repository>@sha256:<64 hex digits>`, the only form gaon's Node Agent starts; the operator refuses to start on any other |
| `AGENT_CPU`, `AGENT_MEMORY` | the MicroVM's requests and limits, each in gaon's quantity form (`1`, `500m`, `1Gi`); gaon's Node Agent refuses a MicroVM that names no cpu or no memory, so the operator refuses to start without either and publishes no figure |
| `GUEST_MACHINE_URL`, `GUEST_CONTROL_URL` | optional: the addresses the adapter in the guest reaches garam and the control service at; each defaults to `GARAM_MACHINE_URL` and `CONTROL_URL` |
| `HEALTH_ADDR` | optional, default `:8081` |

### Key files

The operator cannot import garam's `platform/internal/keyfile`, so
`internal/gaon/keyfile` applies its rule (garam ADR-0015): a key file readable by
any group or other user, or owned by another user, is refused, with the
mode and owner read from the open descriptor. It holds the operator's
key and gaon's client key; the operator stops at start naming the path
and never the contents. The trust roots are certificates and are read
plainly.

### The guest supervisor and image

`cmd/gaon-guest` is the entrypoint of the composed guest image
(garam ADR-0151 §3, its user by garam ADR-0153). `internal/gaon/guest` holds it, and the
constants the operator's MicroVM spec and the supervisor both name (the
accounts, the credential, memory and workspace paths, port 8088), so
each is written once.

- **Accounts.** The MicroVM's `user` is `0:0`, so the supervisor is
  root. It starts the runtime and the adapter as 65532:65532 with no
  supplementary group and so no capability, and the workspace as root
  through `setpriv --bounding-set` keeping the standalone runner's five
  (`SETUID`, `SETGID`, `CHOWN`, `SYS_ADMIN`, `KILL`) with
  `SHERLOCK_EXEC_UID=10001`. Before it starts anything it makes the
  memory directory and the outbox 0700 and 65532's, refusing a path that
  is a symbolic link. The credential files are 65532's at 0400, which
  the operator's `files` entries state as owner. The model credential is
  named by `GUEST_RUNTIME_ENV` and reaches the runtime alone.
- **Order.** `Run` starts the runtime, the workspace and the adapter in
  that order, opens the readiness port 8088 only once all three are
  started, and on SIGTERM or the first program's exit sends SIGTERM to
  each still-running program in start order, waiting for each before
  the next, as garam's standalone runner does.
- **Settings.** The environment names `GUEST_AGENT_GRN` (a canonical
  agent GRN), `GUEST_ASSIGNMENT_EPOCH` (a canonical decimal),
  `GUEST_MACHINE_URL`, `GUEST_CONTROL_URL` and optionally
  `GUEST_RUNTIME_CONFIG`, `GUEST_RUNTIME_EGO` and `GUEST_RUNTIME_ENV`.
  A program gets only
  `PATH`, `SSL_CERT_FILE` and `SHERLOCK_TOOLS_DIR` from it, plus what
  its command needs.
- **The image** (`build/guest.Dockerfile`) is the workspace image with the
  runtime's tools and CA bundle, garam's binary and `gaon-guest` copied
  in, and no `USER`. The three source images are pinned by digest as
  `ARG`s and recorded as `sh.garam.guest.*-image` labels. The build
  stops if the runtime image's `/sherlock` and the workspace image's
  differ. The pins are Sherlock 0.10.0 for the two sherlock images and
  garam 0.8.0 for the server; garam's `platform/Makefile` at
  `568c6e6` pins Sherlock 0.9.0 for the runner's tests, so these
  digests are pinned nowhere else in this repository.
- **Building it.** `make guest-image` builds `build/guest.Dockerfile`
  with the repository root as context, as
  `garam-agent-operator/guest:<REVISION>`, and pushes nothing; the
  operator's image is `garam-agent-operator/operator:<REVISION>` from
  `make operator-image`. The three sources are in a private registry, so
  the build needs a login that can pull them and `make ci` does not run
  it. `release.yml` logs in to that registry with the role that pushes,
  which reads the three, builds both images through `make build-images`
  and pushes them under the release version beside the control
  service's (`delivery.md`, ADR 0084). The operator's `AGENT_IMAGE` takes the guest image as
  `<repository>@sha256:<digest>`, and the release prints that digest.
- **Running it.** `make guest-run` builds the image
  with `make guest-image` and starts it
  as one container with user `0:0` and `SYS_ADMIN` added.
  The entrypoint, the environment and the credential files are what
  `render.go` renders for one agent, written out by `cmd/gaon-render`
  through `agent.Render` and not typed in `tests/gaon/guest-image.sh`: it resolves
  each `valueFrom` from the rendered Secret, or from a file standing for
  the deployment's model Secret, and stops on a source it cannot
  resolve, on a command of more than one word and on an argument. It
  renders a mock-provider configuration and an ego, and shows the
  runtime started with `--config` and `--ego-file`, the configuration
  readable as 65532 and naming `MODEL_API_KEY` without the key, and the
  runtime still running; it does not show the runtime using the ego. It
  checks the start order, the
  readiness port and the SIGTERM drain, each program's user and
  capabilities, that the model credential is in the runtime's
  environment alone, and that a command run as 10001 is refused the
  store, the outbox, each credential file and the runtime's and the
  adapter's environment while 65532 reads the same. It needs Docker, a
  registry login and openssl and is not part of `ci`.

The guest still does not isolate the workspace process from the store:
it is root in the same guest and holds `CHOWN` and `SETUID`, so it could
take the store or become 65532. The runtime and the adapter share one
account, so the runtime can read the adapter's tokens. What gaon's guest
kernel admits for the workspace executor (the user and network
namespace clone, refused under Docker's default seccomp) is not known.

Open in the guest: the supervisor does not reap orphaned Exec children
that reparent to it.

Tokens: `agent-token` is the adapter's agent token and `control-token`
is the agent token minted with the control service as audience, which
the adapter presents to the lifecycle authority (garam's adapter.md §Token
mode). The placement token is separate, sent in `Garam-Placement`. The
operator mints it and delivers it as the file `placement-token`, as
above.

### What the deployment must create

- **Each organization's model-key Secret**: a gaon `Secret` named by
  the agents' `apiKeyRef`, holding the key, labelled
  `garam.sh/role=model-key` and `garam.sh/organization=<digest>`. The
  digest is the first 32 hex digits of the SHA-256 of the organization's
  GRN, `grn:root/iam/org/<id>`, the value the operator labels that
  organization's own objects with. For the organization id `org1` the
  string is `grn:root/iam/org/org1` and the digest is
  `37a401b54d687a3e792937666c548e11`; a test pins that example to the
  code. It is computed with this command:

  ```sh
  printf '%s' 'grn:root/iam/org/<id>' | sha256sum | cut -c1-32
  ```

  The operator creates none and never uses or logs a value. A name is
  one Secret, labelled for one organization. What gaon's files say of a
  Secret that changes after a MicroVM exists is under §What is not
  established.
- **A gaon role for the operator**, created and bound by an
  administrator to the certificate with CN `garam-gaon-operator` and no
  Organization. In the compute group it holds `list`, `create`,
  `update` and `delete` of `microvms`, `volumes` and `secrets` limited
  to `garam.sh/role=agent`, `watch` of `microvms` limited the same way,
  and `list` of `secrets` limited to `garam.sh/role=model-key`. It
  holds no `get`, because the operator reads by list, and no right in
  the RBAC group. A list of the model-key Secrets returns their data to
  the operator, which drops it on arrival; the operator is trusted not
  to use it. `tests/pairrun/pair-run.sh` states this role and binds it.
- **The trust roots and the key files** the operator itself reads
  (§Settings).

### What is not established

- **A real mvmd and a guest.** `make pair-run-gaon`
  (`tests/pairrun/pair-run.sh` with `PAIR_RUN_GAON=gaon`) runs the operator
  against gaon's own services, built from commit
  `2387cebd0b44b322b7da2ff485144a7456a97c6f` of gaon: the API server
  with `--authorization-mode=RBAC`, the controllers, the scheduler, a
  Node Agent with `gaon-storage-local`, and the operator's client
  certificate bound to a role (§Settings, §What the deployment must
  create). The Node Agent runs against a minimal mvmd of
  `tests/pairrun/gaonharness`, which keeps Machines and Disks in
  memory and starts nothing. The run shows the MicroVM `Running` and
  `Ready` as that Node Agent reports it, and shows no guest program: the
  three programs of the composed guest image never ran.
- **A get refused for lack of labels, and the operator's own watch.**
  The role grants no `get`, and a get of a MicroVM or a Secret by name
  is refused for that, which says nothing about a label. The operator
  lists and writes and never watches; the role's `watch` is answered,
  and no operator behaviour exercises it.
- **Garbage collection by `ownerReferences`.** The operator sets none
  (§Open questions).
- **A first placement only.** `previous` is always null.
  `writerStoppedSha256`, the SHA-256 of the canonical JSON of the stop
  observed on an earlier MicroVM, is not built, so a replaced MicroVM is
  not registered.
- **A changed configuration, and a changed Secret or ConfigMap.** The
  operator writes only `runStrategy` to a MicroVM that exists
  (`internal/gaon/agent/service.go`), so a changed `configuration` or
  `apiKeyRef` reaches none. For a Secret or ConfigMap that a `valueFrom`
  names and that changes, gaon's files at commit
  `2387cebd0b44b322b7da2ff485144a7456a97c6f` state, and none of it was
  observed here, since no guest has run under gaon:
  - an `env` entry: it is resolved when the MicroVM starts and not
    updated while it runs (`lib/api/microvm_process.go`, the comment on
    `ValueSource`; `docs/architecture/resource-api.md`, the `env`
    bullet of §MicroVM). The Node Agent resolves `env` and `files`
    again at each start of a machine it already holds, so a changed
    value reaches an `env` entry at the next start and never a running
    guest's (`docs/architecture/node-agent.md`, the Run strategy and
    resolution bullet).
  - a `files` entry: its value is replaced in the running MicroVM when
    the object changes (`lib/api/microvm_process.go`, the same comment,
    citing ADR 0029). The file is read again while the MicroVM runs
    and replaced when the value changes
    (`docs/architecture/resource-api.md`, the `files` bullet of
    §MicroVM); a running guest gets a changed file by that replacement
    (`docs/architecture/node-agent.md`, the same Run strategy and
    resolution bullet).

`make pair-run` runs the same steps with `tests/gaon/gaonstandin` in place
of gaon, which serves `tests/gaon/gaonfake` on a port. Its garam is the
`garam` binary, the migrations and the test-principal fixture that
`make garam-e2e` copies out of garam's test image at
`43d700d30910319095b7f97230bffc79c2328981`, run as host processes over a
PostgreSQL container; its control service is built from `cmd/control`
of this repository. It prints 54 checks, the number the same steps had
in garam at `568c6e6`. It shows none of
gaon's behaviour above and states `Running` through an admin path that
is not gaon's.

## What is not built

- The deployment of the operator, and the `NetworkPolicy`.
- Renewing an agent's token by restarting its MicroVM.
- Replacing a MicroVM of another revision.
- The agent's instructions file and message issuers.

## What the tests prove

They run the reconciler against stand-ins for the feed and for garam,
and the wire clients against `tests/gaon/garamfake`, an `httptest` server of
the machine listener's three operations that checks an assertion as
garam ADR-0151 §6 says garam does, and against `tests/gaon/gaonfake`, an
`httptest` server of gaon's REST paths built on `lib/api`'s types and
validation. The fake enforces
compare-and-swap on `resourceVersion`, the API's validation and
label-selector lists. It does not prove: RBAC selector rules, garbage
collection, the Node authorizer, a get refused for lack of labels,
`bindingEpoch` as gaon writes it, or watches.

The reconciler's tests prove a first placement registered once with the
two uids and the token's digest, a repeat read as a repeat, a stopped
agent not registered, a superseded body (both kinds) never presented
again and a new body registered, another 4xx presented again after five
minutes and a 5xx failing the pass. They prove the rendered MicroVM
carries the configuration and ego files and each agent's own key
reference, two agents rendering apart, no `MODEL_KEY_*` setting, and a
bad configuration writing nothing. The control client's tests prove the
body, the headers and the sorting of each refusal.

The supervisor's tests start helper processes and prove the start
order, the readiness port opening after all three start, the drain
order with a wait for each, the drain after one program exits early,
the settings and account each program receives, and a store path that
is a symbolic link refused. `guest-run` shows in the built image each
program's user and capabilities and a command run as 10001 refused,
started from what `render.go` renders.
They do not prove the adapter's calls to garam, which need an admitted
agent, an Exec under the guest kernel, or gaon's MicroVM start.

## Rationale

The operator lives here, in the repository that owns the control service
and the controller identity it registers under, so one commit changes the feed and its
reader together; ADR 0082 records the move and what was kept of garam's
decisions. The reconciler declares its two outside dependencies; the wire clients
fill those interfaces. It deletes nothing because nothing in the feed
says an agent is gone for good.

That the operator's image and the guest image are published, as
`operator` and `guest` beside `control` and by the same workflow, is
ADR 0084.

## Open questions

- Whether the MicroVM should carry an owner reference to its Secret and
  Volumes, which waits on gaon's garbage collector being verified.
