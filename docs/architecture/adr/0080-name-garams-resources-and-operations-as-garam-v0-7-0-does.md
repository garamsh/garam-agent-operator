# ADR 0080: Name garam's resources and operations as garam v0.7.0 does

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #411, filed on the owner's decision of 2026-11-13 recorded at `garamsh/aurora#749`. The published control image 0.3.0, started against garam `v0.7.0` (`100083b78b173f0439972840563b8f052d6438cf`), refused every console request with "operation authority binds another organization" (`garamsh/garam#1813`): it compared an organization's GRN to `grn:root:default:org:<id>` and an operation to `agent:create`, and garam's introspection answers `grn:root/iam/org/<id>` and `iam:agent.create`.

garam changed two things, both first released in `v0.7.0`:

- **The GRN grammar**, garam's ADR-0142: `grn:<org>/<service>/<type>/<id>`, an organization `grn:root/iam/org/<id>`, an agent `grn:<org>/iam/agent/<id>`, an operator `grn:<org>/iam/operator/<id>`. In a URL path a GRN is one percent-encoded segment.
- **The action names**, garam's ADR-0145: `<service>:<type>.<verb>`. The ten garam stores, from its migration `000072_iam_action_dot_form`: `iam:agent.create`, `iam:agent.configure`, `iam:agent.activate`, `iam:agent.recover`, `iam:agent-template.read`, `iam:agent-template.publish`, `iam:execution-profile.read`, `iam:agent.execution-read`, `iam:operator.read`, `iam:agent.replace`.

ADRs 0057, 0074 and 0076 state the old operation names as part of their decisions. ADR 0063 does too and is already superseded by ADR 0069. ADRs 0009, 0022 and 0055 name the old GRN form only in what they observed or migrated, and decide nothing by it.

## Decision

This project compares and sends garam's GRNs and operation names in garam's current form: the console and controller APIs check `iam:agent.create`, `iam:agent.configure`, `iam:agent.recover`, `iam:agent-template.read`, `iam:agent-template.publish`, `iam:execution-profile.read`, `iam:agent.execution-read` and `iam:operator.read`, and an organization's GRN is `grn:root/iam/org/<id>`. The operation names in ADRs 0057, 0074 and 0076 are superseded by this one; nothing else in them changes.

## Consequences

- **Rows the control store holds under the old names are not converted.** They record the operation an authority bound. A repeat of a request recorded before this change compares its authority against the old name and is refused; ADR 0069 keeps no upgrade path for the store during development.
- **Authorities minted by garam before `v0.7.0` are refused here**, as garam's own migration rewrote the ones it held.
- **The control image moves with garam.** A control image built before this change serves no console request against garam `v0.7.0`, and one built after serves none against an earlier garam.
- **Frozen ADR bodies keep the old forms.** `control.md` is where the current forms are stated.
