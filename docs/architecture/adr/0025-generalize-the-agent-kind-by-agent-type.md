# ADR 0025 — Generalize the Agent kind by agent type

## Status

Accepted. Settled on issue #169 after a measurement that this controller is
asked for every supported agent binary, not only sherlock. Implements ADR 0025
on issue #169, supersedes nothing.

## Context

The operator reconciles an `Agent` into a Pod. Today the Pod is built around
the sherlock agent binary: every environment variable this operator writes
carries the sherlock prefix, the config-file directory under the operator's
mount is the sherlock project segment, and the tool tree, the workspace and
the credential copy are wired around sherlock's split between an agent
container and a workspace container. The agent kind name (`Agent`) was chosen
to be the generic name; the binary it carries is what makes the build
sherlock-shaped.

The operator is asked to support claude-code and codex as well. The shape
that scales is a single `Agent` kind with a closed set of admitted types and
a per-type dispatch in the controller. Three answers were considered:

1. **One kind per agent.** A `SherlockAgent`, `ClaudeCodeAgent`, `CodexAgent`
   kind, each with its own controller. The kinds duplicate the fields that
   are not type-specific and multiply the controllers for what is, today,
   one workload shape with one divergent variable prefix.
2. **`spec.template` embedding a `PodTemplateSpec`.** Generalizes past the
   three types we know about to anything the operator can be told about. The
   contract it commits to is too wide: every reader of an `Agent` would have
   to be type-aware, and the operator's name for the workload's shape would
   stop being a rule.
3. **`spec.type` plus a per-type table inside the controller.** A closed
   enum, validated at admission; a lookup the controller runs once per
   reconcile; per-type literal generation when a non-sHERlock descriptor is
   filled in. One kind, one controller, one schema.

## Decision

Add `spec.type` to `AgentSpec`. It is optional, defaults to `sherlock`, and is
validated at admission against the closed set `{sherlock, claude-code,
codex}`. An unknown type is refused at admission by the kubebuilder
validation; an admitted type that the controller has not yet learned to build
is refused on reconcile with `Synced=False, Reason=TypeUnimplemented`.

The controller carries a `map[string]agentTypeDescriptor`. Each descriptor
holds the environment-variable prefix and the config-file directory segment
the controller writes. Today's descriptors:

| Type | Descriptor status |
|---|---|
| sherlock | implemented; `envPrefix=SHERLOCK_`, `configDirName=sherlock` |
| claude-code | unimplemented; zero-value descriptor; reconcile refused |
| codex | unimplemented; zero-value descriptor; reconcile refused |

The controller reads the descriptor once per reconcile and dispatches the
rest of the build through it. Today's per-type literal generation lands with
the first non-sherlock descriptor; the parameter is carried through now so
that change does not move a signature.

The `Agent` kind name is unchanged. A reader of an `Agent` continues to read
the type-specific fields from `spec.image`, `spec.tools`, `spec.resources`
and the operator's own configuration — the type only resolves the env-prefix
and config-directory-name the controller writes.

## Consequences

- A new agent binary is a code change, not a configuration change. The
  operator's PM signs off on adding a descriptor; the agent's PM signs off
  on the env-variable contract. Two halves, one for each side, no hidden
  ambiguity.
- The CRD validation moves the unknown-type refusal to admission, where a
  reader of the schema learns it without waiting for a reconcile. The
  reconcile-time refusal is only for types that are admitted but
  unimplemented, which is a controller version rather than a user error.
- The default is sherlock because every `Agent` before this field was
  already sherlock-shaped. Changing the default is the same one-line change
  as adding a descriptor; the closed set decides what is supported, the
  default decides what an unset field means.
- The kubebuilder-marker schema generation produces the enum directly. No
  second source of truth; the API server is the enforcer.

## What this does not decide

- Whether claude-code and codex are the right names. The closed set is
  shaped by the names the agent projects settle on; if they settle on
  different ones, the enum is updated.
- Whether `spec.image` should be split into per-type fields. It is not
  split; the field names any image, and the operator's per-type default is
  overridable.
- The exact environment-variable contract for claude-code and codex. Those
  land with the descriptors for those types, in follow-up PRs, when the
  agent projects have settled their own settings.