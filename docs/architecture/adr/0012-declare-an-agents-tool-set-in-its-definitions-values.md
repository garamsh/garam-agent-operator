# ADR 0012: Declare an agent's tool set in its definition's values, and carry the keys this operator knows into a file

> Status: accepted
> Date: 2026-08-27

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Nothing in this repository reads a definition's values. `internal/garam/client.go:199` parses them, `internal/garam/garam.go:24` models them, and outside `internal/garam/` the tree names `Values` nowhere. The boundary is structural rather than incidental: `internal/garam/constructor.go:22` hands `Construct` a GRN and a credential, and `internal/garam/poller.go:58` drops the rest of the definition at the call, so a value cannot reach construction even where a reader wanted one.

So an agent's tool set is declared nowhere its author can reach, and this ADR decides where it is declared. **It decides nothing else. No mechanism lands with it, and this repository reads no value after it merges** — both peer projects sequenced it that way, because a declaration site decided after the code is written turns the code into work to delete.

**This extends [ADR 0009](0009-construct-a-claimed-agent-from-the-operators-own-configuration.md) and supersedes nothing.** ADR 0009 says: "No value of a definition names either, and none is read." Read with its subject, *either* is the two flags that paragraph decides — `--agent-image` and `--agent-storage-size`, "neither with a default". The reason it gives is confined to them: "a console user choosing an agent's container is choosing what this operator runs as a root-capable workload in a cluster they cannot see." Its rejected alternative names one key rather than the map — "reading the image out of a definition's `values`, in every form" — and a blanket prohibition on reading any value would have been shorter to write than either sentence. ADR 0009 also names the shape a later decision would have to take: "Per-agent configurability, if it is ever wanted, is a schema change with an ADR of its own and not a convention about a map key." This is that ADR.

**The division both documents record already puts a tool set on `garam`'s side.** `docs/architecture/agent.md` drew it in one sentence before this decision: composition is `garam`'s — the model, the system prompt, the workspace, the names of credentials — and construction is this operator's. A tool set is composition, and one of the four things already listed there, the names of credentials, is something a definition carries in `values`. The division was already describing values this operator would one day carry; what it left open is which of them, and what becomes of the rest.

**The strongest objection is ADR 0009's own reason, and it is met rather than dodged.** A tool a pin names has to exist in the image this operator chose, so a tool set does assert something about a container its author cannot see. What separates it from the image is what a wrong answer buys. Choosing the image chooses what runs; choosing among the tools an image already carries chooses what the agent may do inside it, and a pin naming something absent introduces no code — the worst it can do is leave the agent refusing to start, which is `gagent`'s check to make and not this operator's. One answer is a privilege and the other can only narrow. What a pin naming a tool an image does not carry does exactly is not measured here. What remains is that whatever `gagent` answers happens where the author cannot see it, and this operator reports nothing back to `garam`; that is already an open question in `agent.md`, and this decision gives it another subject rather than closing it.

**What the consuming side accepts was measured rather than read.** `gagent`'s PM ran it against that project's real entry point — `AddSharedFlags`, `AddAgentFlags`, `Resolve` — on 2026-08-26, and recorded the results on issue #92:

| Setting | From a file | From the environment |
|---|---|---|
| `tools-dir` | yes | yes, `GAGENT_TOOLS_DIR` resolved |
| `tools.pins` | yes | no, and the attempt is fail-closed |
| the required set | no path at all | no path at all |

The two are not symmetric, and `GAGENT_TOOLS_PINS` is worse than unsupported: viper's `AutomaticEnv` makes the key present and unmarshals the map empty, the pin check then rejects it, and the agent refuses to start — the operator names two tools and the error says it named none.

**The second row holds for as long as `gagent#745` is open, and for nothing longer.** That is where the result is recorded and it is a runtime behaviour rather than a line anyone can read, so the issue is what a reader checks it against: a `gagent#745` found closed is notice that the row may have gained a second road, and this ADR's file rule is the sentence to re-check.

The third row is read rather than received: `gagent@ff9a2dc:internal/tool/registry.go:22` is `var DefaultRequiredTools = []string{"message_send"}` and `gagent@ff9a2dc:cmd/gagent/agent.go:116` passes it into the agent. No file key, no environment variable, no flag reaches it.

**What this repository can place.** It sets no `Env` on either container of an agent's workload: `grep -n "Env\b" internal/controller/agent_statefulset.go` answers nothing. It already places a file — `internal/controller/agent_statefulset.go:173-181` is [ADR 0010](0010-copy-an-agents-credential-into-a-memory-volume-the-pods-own-user-owns.md)'s init container copying a projected volume into one the agent reads.

## Decision

**An agent's tool set is declared in its definition's `values`, and this operator reads a closed set of keys out of them.** The set today has one member: the key family `tools.pins.<name>`, where the suffix is a tool's name and the string is its pin. `garam` stores the keys and the strings and interprets neither (`internal/garam/garam.go:21-24`), so the key format is this operator's contract, and it borrows `gagent`'s own setting name because this operator has to write that setting anyway — a name of its own would be a second name for one thing plus a table mapping them, which is `simplicity.md` §An abstraction that hides nothing.

**The name is the key's and the pin is opaque.** This operator reads structure out of key names only; what a pin means is `gagent`'s, and nothing here validates one. The alternative — one key whose string encodes the whole set — was rejected because it makes this operator either a templater of somebody's text into a configuration file or a parser of a format it has no reason to own, and a malformed one then fails at the agent with an error nobody can trace back to the field that was typed.

**Only keys this operator knows are read, and every other key is ignored.** Carrying the whole map is the road not taken, and its failure mode is the one ADR 0009 wrote its prohibition against: `values` is free-form, so a pass-through operator has no way to refuse an `image` key — "nothing in `garam` would reject an `image` key and nothing here would notice" — and the rule would be kept by nobody typing the wrong thing. It also makes every key anyone ever typed part of the workload's configuration surface, where a typo is a silently honoured setting and a secret pasted into a field is a file in a Pod. The closed set costs what a closed set costs: a definition can declare only what this operator has already been taught, so a new setting needs a release here before it can be declared. That is the same cost `AgentSpec` already pays, and it is the one taken.

**`tools-dir` is not one of the keys, because it is construction.** The directory an agent loads tools from is a path inside a container this operator chose, and a definition naming it would let a console user point the agent at a directory whose contents they control — ADR 0009's reason for the image, arriving in another form. This operator decides it and writes it beside the pins.

**A value this operator does not understand is ignored and reported, and construction goes on.** Refusing to construct would turn a typo in a console field into an agent that never runs, over a value that by construction has no meaning here, and this operator cannot tell a typo from a key a later release understands. The report goes where the poll's per-pass detail goes rather than where its state changes go, because a definition carrying a stray key reports it on every pass and a poll has no memory.

**A tool set that is absent leaves nothing placed, and no default is invented here.** An empty pin set and no pin set are different inputs and only `gagent` decides what either means, so placing an empty one would be this operator asserting a meaning it does not hold. It is the reason `spec.image` already gives for having no default, one layer out: the field asks rather than invents.

**The declaration reaches the workload as a file, and this operator writes no environment variable for it.** As measured, the file is the only road the pins have, and the environment road produces a fail-closed agent and an error blaming the operator; that measurement holds while `gagent#745` is open. Should `gagent#745` close with an environment road, what changes is why this decision stands and not the decision: forced becomes chosen, and the ground under it is the one below rather than the measurement. One road rather than a per-key choice, because a table of which key travels which way is a second thing to keep correct for a set with one member. That this repository already knows how to place a file is a convergence and not the reason — the environment road is small new work, and had the measurement gone the other way it would have been worth doing. What would reopen the choice is a key this operator has to carry that cannot be a file.

**The file is not a credential and is not treated as one.** ADR 0010's memory-backed volume and owner-only copy exist for a rule about key material, and none of it applies here: a pin set is public, so it needs no copy off the projection and no mode only its owner can read. Reusing that machinery whole would be paying for a guarantee nothing asks for.

**No key this operator reads carries a credential or a credential's name.** `garam`'s ADR-0036 — received through issue #92 rather than read here — permits a `values` key to carry the *name* of a credential and never its value. The closed key set is what makes the statement checkable rather than hopeful: this operator carries one family of keys and that family is pins, so there is no key through which a value could arrive and be mounted. A pin string this operator cannot interpret could hide anything, and what keeps a secret out of `values` at all is `garam`'s rule, not a check here.

**The `Agent` is what carries the declaration to the workload, and one a person wrote declares a tool set the same way.** The reconciler reads the `Agent` and nothing else, so a tool set reaches a Pod through its spec or not at all. It is a field of the spec rather than a second object the spec names: the credential's second object exists for a secrecy rule this does not meet, and an object here would buy an extra name for this operator to invent and an extra permission to hold. A field only the poller could fill would make a constructed agent a different kind of thing from a written one, and `v1alpha1` serves both.

**`Construct` receives the `Definition`, and only because the `Definition` stops carrying a free-form map.** The widening is not the decision; what makes it safe is. `runtime-safety.md` §Trust boundaries settles it rather than this ADR inventing an answer: the map is a third-party API response, it is parsed once where it is read out of `garam`'s answer, and what travels past that boundary is the typed selection — so "the rest of the codebase cannot accidentally pass the raw form." Then the constructor holds no map, cannot name an `image` key, and the prohibition ADR 0009 had to write as a rule becomes a property of a type. Widening `Construct` while `Values` stays raw is the thing to refuse, and it would hand the one writer of `AgentSpec.Image` a map with an `image` key in it. The parameter count falls rather than rises: a `Definition` carries the GRN.

**A definition declares the pins and cannot declare the required set, and that limit is stated as a limit.** No configuration in `gagent` reaches `DefaultRequiredTools`, so nothing this ADR decides can carry one, and this operator reads no key for it: a key it accepted and could not deliver would report success while nothing changed.

**Two propositions sit under that limit and only the first is settled.** That `message_send` is mandatory is an intended invariant — an agent that cannot report is fail-closed by design, which is `gagent`'s own ADR 0009 (received on 2026-08-27 through this repository's PM, not read at a commit). Whether it is right that the invariant is reachable from no configuration, and visible in none, is decided by nobody: not by `gagent`, not here. The first does not settle the second, and this ADR asserts neither that the limit should be lifted nor that an intended invariant makes the limit unremarkable. It records that a definition's author cannot see it and cannot name it, and leaves the question where it is.

## Consequences

Easier: an agent's tool set is declared where the agent is, so changing it is an edit in `garam`'s console rather than a flag on every operator deployment, and a fleet of agents with different tool sets needs one operator rather than one deployment each. Nothing is asked of `gagent`: both settings resolve from a file it already reads, measured on issue #92, so the chain needs no new reader on the consuming side.

Harder, and this is what the decision gives up: **this operator becomes a writer of another project's configuration format.** A change to how `gagent` reads its pins is a change here, and `gagent#745` is already one measurement that moved a design. The spec field also asks an agent's image to be one that reads that configuration, which `spec.image`'s doc comment is where such a requirement is stated — `agent.md` already records that an image owes the Pod's user in the same way.

**A declared tool set can be wrong in a way this operator cannot see.** A pin naming a tool the image does not carry is answered by `gagent` — refused or ignored, and which of the two is not measured here — in a cluster the definition's author cannot look at, and this operator reports nothing back to `garam`. Neither answer runs anything the image did not already carry, and what would make either visible is the reporting `agent.md` already holds as an open question.

**A value nobody here understands does nothing and says so only in this operator's log.** The person who typed it is working in a console and does not read that log. That cost is accepted because the alternatives are worse — refusing construction over an uninterpretable value, or a status field for a report `garam` is the right audience for.

**The required tool set stays where it is.** Every agent this operator constructs carries `gagent`'s built-in required set, whatever its definition declares, and no part of this decision changes that.

Ruled out: **carrying every value through to the workload**, which makes `values` an open channel into an agent's process configuration and leaves ADR 0009's prohibition resting on nobody typing `image`. **A single encoded key** holding the whole tool set, which makes this operator parse or template a format it does not own. **Reading `tools-dir` from a definition**, which is the image decision wearing another name. **An environment variable for the pins**, measured to produce a fail-closed agent and an error naming the wrong party for as long as `gagent#745` stands. **A second object holding the tool set**, which buys a name and a permission for a value that is not secret. And **widening `Construct` to a `Definition` that still carries a raw map**, which would put a free-form map in the hands of the one writer of an agent's image.

Not decided here: **which object renders the declaration into a file in the Pod.** It is not secret, so it needs neither a Secret nor ADR 0010's memory copy, and the choice is a mechanism the implementation issue makes. Not decided here either: **a second key family**, which arrives when a second setting is worth declaring, and **what an operator does with a key a later release would understand**, which is the same forward-compatibility question the ignore rule answers for now.

## Errata

### 2026-08-27 — a public pin set still takes a mode

Decision rejects reusing ADR 0010's machinery with this: "a pin set is public, so it needs no copy off the projection and no mode only its owner can read." The first clause holds. The second does not follow from it and is wrong.

`gagent` refuses its config file unless its owner alone can read it: `gagent@04ed05a:internal/config/config.go:294` requires it of the file it loaded, and `gagent@04ed05a:internal/config/owner_only.go` refuses every mode that grants a bit outside the owner. The pins are a `gagent` setting and resolve from that file — which is what this decision borrows the setting name for — so the file this decision creates does take a mode only its owner can read, and an agent handed one that does not refuses to start.

The mode is not asked for secrecy, and that is why the first clause survives being read as deciding the second. `owner_only.go` gives the reason: the config file and the TLS key are the operator's material in the runtime user's domain, so a child running as the agent must not read them. It is the same management-ownership ground the environment road is refused on, arriving a second time at the file.

The decision stands whole: the declaration reaches the workload as a file, the file is not a credential, and none of ADR 0010's memory-backed copy is taken — that half answers a rule about key material and a pin set is not key material. What does not follow from a file's contents being public is that the file needs no mode.

Falsified on issue #96.
