# ADR 0029: Route every agent-specific name in the Pod through its type's descriptor, and keep the operator's images out of it

> Status: accepted
> Date: 2026-09-27

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

[ADR 0025](0025-generalize-the-agent-kind-by-agent-type.md) added `spec.type` and a per-type descriptor in `internal/controller/agent_types.go` carrying two fields, `envPrefix` and `configDirName`. The Pod builder did not read either. `internal/controller/agent_statefulset.go` held sherlock's names as package constants: three mount paths, the config directory, the config file's layout and its keys, four `SHERLOCK_` variables, `XDG_CONFIG_HOME`, and the workspace's address. A second type given a filled descriptor would have received sherlock's Pod.

The two fields could not have carried those names on their own:

- **A prefix does not produce a variable name.** `SHERLOCK_WORKSPACE_ADDR` is the prefix plus sherlock's setting `workspace-addr` under sherlock's dash-to-underscore mapping (`sherlock@8218189:docs/architecture/deployment.md:82`). The setting names are the agent's as much as the prefix is, so the descriptor has to hold the names.
- **A directory segment does not produce the file.** sherlock resolves `os.UserConfigDir()/sherlock/config.yaml` (`:84`). The file name is sherlock's layout too.
- **The file's keys are the agent's.** `tools.pins` is a sherlock setting (`:92`), so the text the init container writes is sherlock's text.

Every name kept for sherlock is cited to `sherlock@8218189:docs/architecture/deployment.md`, or to the source line the Pod builder already cited, at the descriptor field that holds it.

Images are the other question issue #176 asked. The agent's image is `spec.image`, per `Agent`. The copy and workspace images come from `--agent-copy-image` and `--agent-workspace-image`, and `--agent-image` names the image a constructed `Agent` gets.

## Decision

**The descriptor carries every name the Pod builder writes that belongs to the agent binary, and the Pod builder reads them from it.** For sherlock that is:

- the credential copy's mount path and the Secret projection's mount path
- the state volume's mount path
- the configuration directory, the variable the agent reads it from, and the config file's path under it
- the function that renders the config file's text from an `Agent`'s spec
- the workspace's address, and the four variables carrying the two ends of the link, the workspace's directory and its exec uid

`envPrefix` and `configDirName` are removed. The full variable names and the file path replace them.

**Every field is required.** A descriptor with any field unset is refused on reconcile with `Synced=False, Reason=TypeUnimplemented`, the refusal ADR 0025 gave an empty one. The check reads the fields by reflection, so a field added later is required of every type without a second list. `claude-code` and `codex` stay empty and stay refused.

**What stays the operator's is not in the descriptor.** Container and volume names, the Pod's user and group, the security context, the credential's modes, `AGENT_CONFIG_CONTENT`, the owner-only mask, the workspace's subdirectory name under the state volume, and pull-always on every container are the same for every type. None of them is a name the agent reads, so a new type does not change them.

**Images are not per type, and no flag changes.** `--agent-copy-image` asks for a shell, `install` and `mkdir` and nothing of the agent, so it serves every type. `--agent-image` and `--agent-workspace-image` name sherlock's images today because sherlock is the only type built. When a second type is implemented, its issue decides whether those two become per type, and how, along with its runtime contract. Renaming a flag is out of scope here.

## Consequences

- Implementing `claude-code` or `codex` means filling a descriptor. It does not mean editing `agent_statefulset.go`, except where the new type's Pod has a different shape.
- A type whose Pod has no workspace container cannot fill the four workspace fields. That type's issue changes the descriptor's shape: an optional workspace part, or a second descriptor kind. That is a new decision, not something this one allows.
- A `type: sherlock` Agent's StatefulSet is unchanged. `applyAgent` was run on `dev` at `08f39b2` and on this change for an Agent with no workspace and no pins, and for one with both. The two outputs were serialized and compared, and they are byte-identical.
- ADR 0025's table of descriptor fields no longer describes the struct. The fields are the ones listed here, and ADR 0025's decision stands otherwise: the closed set, the default, and the refusal of an admitted type that is not implemented.

## Rejected alternatives

- **Compose names from `envPrefix` plus setting names.** The setting names are the agent's, so they would still be in the descriptor, split across two fields and joined by a mapping that is also the agent's.
- **Per-type image flags now.** They rename or multiply flags for types nobody can build, which is out of scope for issue #176. The type that needs a different image is the one that can say which.
- **Make the workspace optional in the descriptor now.** No type without a workspace exists yet, and `docs/convention/simplicity.md` §Rules asks for an abstraction only when a second real case arrives.
