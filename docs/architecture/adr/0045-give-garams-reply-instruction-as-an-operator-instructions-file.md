# ADR 0045: Give garam's reply instruction as an operator instructions file, behind a switch until the deployed agent image takes one

> Status: accepted
> Date: 2026-10-05

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #239. [ADR 0041](0041-join-garams-reply-instruction-to-the-ego-wherever-the-adapter-is-placed.md) joins `garam`'s reply instruction to the ego file wherever the adapter is placed, because at `sherlock@2ad4c13` the ego was the only text `sherlock` took from its operator. It recorded the cost: an agent with the adapter and no declared ego gets the instruction as its whole ego file, and so loses `sherlock`'s default ego. The instruction also sits inside text a reader takes as the agent's persona.

`sherlock` accepted a separate input as `garamsh/sherlock#949` (its ADR 0024). `sherlock@a44bbaa` (#951) adds an optional `--instructions-file`, and the change is contained in tag `v0.1.0`. At dev `44aaa55`, `sherlock` describes it as follows:

- **`agent.md`.** The instructions compose the ego (declared, or the embedded default when none is named), then the operator instructions, then the platform contract. "The operator instructions add to the ego rather than replace it."
- **`deployment.md` §Volumes and ownership.** The file is read once at startup, can be mounted read-only, and is not mode-checked. "Naming an instructions file never replaces the ego."
- **`deployment.md` §Configuration.** `--instructions-file` is a flag with no environment or config-file form. A named file that cannot be read refuses startup.

An agent image older than `v0.1.0` refuses an unknown flag at startup. That is the ground on which [ADR 0037](0037-carry-an-agents-identity-in-its-spec-and-start-the-agent-under-it.md) put `--assignment-epoch` behind `--agent-assignment-epoch`.

## Decision

**Wherever the adapter is placed, and the manager's `--agent-instructions-file` is on, `garam`'s reply instruction is the agent's operator instructions file.**

- **The text is unchanged.** It is ADR 0041's text, still a field of the type's descriptor.
- **Where it is written.** The config init container writes it to `sherlock/instructions.md` under the configuration directory, from a variable set on that container alone, under the same `umask` as the other files. The agent is passed `--instructions-file <path>`.
- **The ego file is `spec.ego` alone.** Where the spec declares no ego there is no ego file, and `sherlock`'s default ego applies.
- **The mount.** The file reaches the agent container only, through the configuration volume it already mounts read-only. No other long-running container mounts that volume.
- **No adapter, no file.** Where the adapter is not placed there is no instructions file, whatever the switch says, because no envelope arrives there.

**The switch is off by default, and off keeps ADR 0041's behaviour exactly.** The instruction is joined to the ego, and no instructions file is named. This follows `--agent-assignment-epoch` (ADR 0037): only the owner of `--agent-image` can say the image accepts the flag.

**The default flips once the deployed agent image carries `sherlock` `v0.1.0` or later, as the gitops PM confirms.** That change flips the default to on and removes the joined path with it. Until then the switch is the deployment's to set.

This supersedes ADR 0041. Its placement conditions, its text and its reasons for the manager rendering the instruction (rather than the control service) carry over unchanged. Its joined ego remains only as the switch's off state.

## Consequences

- **The default ego is kept** for a managed agent with no declared ego, once the switch is on. ADR 0041's recorded cost ends there.
- **The ego file holds only what its author wrote,** so the persona and the operator's contract text are separate inputs.
- **Turning the switch on rolls the Pods.** Every adapter-carrying agent's Pod is rolled once, because its arguments and its config container's variables change.
- **Two renderings exist until the default flips.** Both are tested, and the off state is ADR 0041's behaviour unchanged.
- **An image that predates `v0.1.0`, run with the switch on, refuses to start.** That is the reason the switch exists, and why it is not on by default.
- **The instruction is still not seen to work.** Whether a real `sherlock` follows it is #236's joint acceptance, as ADR 0041 recorded.

Ruled out:

- **Rendering the file unconditionally.** Every agent running an image older than `v0.1.0` would stop starting.
- **Detecting the image's version.** An image reference does not say which release of `sherlock` it carries, and nothing here can read it before the Pod starts.
- **Keeping the instruction in the ego beside the new file.** It would reach the model twice, and the ego would still not be the author's alone.
