# ADR 0074: Report each operator's images and last contact to a console read of the control service

> Status: accepted
> Date: 2026-10-14

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

The console is to show which images each of an organization's operators runs, and when each last reached the control service (#357, `garamsh/garam#1047`). garam's API has no field for either, and under garam's ADR-0077 an operator's deployment status is the control service's. garam's half is built: `operator:read`, an operation targeting the organization (`garam@58712f7` `api/machine.yaml` `OperationBinding`). The answer was agreed with garam's PM on #357 and is recorded at `garam@58712f7` `docs/architecture/operators.md`:

- `{operators: [{grn, images: [{name, ref, digest?}], reportedAt?, lastContactAt?}]}`.
- An absent `reportedAt` means the operator never reported. An absent `lastContactAt` means it never made contact. Neither is ever filled with the current time.

What this service already holds:
- **Contact.** Every controller request is authenticated as that controller, on garam's proof ([ADR 0072](0072-authenticate-the-manager-and-the-control-service-to-garam-with-their-own-private-keys.md)). The proof names the controller's organization.
- **Images.** The images a controller runs are its own configuration, never a profile or definition field ([ADR 0007](0007-claim-definitions-from-a-poller-beside-the-reconciler.md)). The flags name four: `--agent-image`, `--agent-adapter-image`, `--agent-copy-image` and `--agent-workspace-image`. No flag names the manager's own image, which kustomize sets on the container.

## Decision

1. **The control service records each controller's last contact when garam proves the controller's session.**
   - **Where.** On every controller route, once the session proof succeeds and before the store is read for the request.
   - **What.** The controller's GRN, the organization the proof names, and the time.
   - **Coalesced.** A contact within one minute of the last one this process wrote for that controller is not written. The feed's long poll brings a controller back at least every 30 seconds, so a write per request would be one every few seconds per controller. A person reads "last contact" in minutes, so one minute is what the console can show. Each instance of the service coalesces on its own.
   - **A failed write fails nothing else.** The contact is bookkeeping for the console, and the request it rides on is how a controller receives desired state. A write that fails is logged once, with the controller's GRN and never its token, and the request proceeds. The failed write is forgotten, so the next request writes again.
   - **Never lowered.** A stored contact is never replaced by an earlier one.
2. **A controller reports the images it runs at `PUT /v1/operators/self/images`.**
   - **Body.** `{images: [{name, ref, digest?}]}`. Each `ref` is the reference the controller was configured with. `digest` is `sha256:` and 64 hex digits, and is given only where the controller knows one.
   - **Refused.** A report naming no image, more than 16, an image without a name or reference, one name twice, a digest of another form, or an unknown field is 400. Nothing is stored.
   - **Answer.** `204`. The report replaces the controller's earlier one, stamped with the time it arrived. A report earlier than the stored one changes nothing.
3. **The manager reports once, after it starts and wins leader election, and asks again until one report is accepted.**
   - **Configured images.** It reports each of the four image flags that is set, under `agent`, `adapter`, `copy` and `workspace`. A digest is taken only from an `@sha256:` reference.
   - **Its own image.** It reads its own Pod, named by `POD_NAME` from the downward API, through the API server. The `manager` container's spec image is the reference, reported as `manager`. Its status `imageID` gives the digest where that ends in a sha256 digest. Where `POD_NAME` is unset, or the read fails, the report goes without the manager's entry, and that is logged once. No value is made up.
   - **Retries.** A 5xx or no answer is sent again after a wait doubling from 1 to 30 seconds, and a 4xx after 5 minutes.
   - **Nothing to report.** Where no image flag is set and its own image cannot be read, it sends nothing, logs that once, and stops: the service refuses a report naming no image, so asking again would never end.
4. **The console reads them at `GET /v1/orgs/{org}/operators` under `operator:read`, targeting the organization.**
   - **Answer.** Every controller of the organization that reached this service, by GRN, in the agreed shape.
   - **Never reported.** A controller that never reported has `images: []` and no `reportedAt`.
   - **Organizations.** A controller another organization holds is in no other organization's answer.
5. **The record is a table of its own, `operators`, added by migration 2** ([ADR 0069](0069-keep-no-upgrade-path-for-the-control-services-store-during-development.md): "The next schema change is migration 2"). It is keyed by the controller's GRN and holds no credential: a reference, a digest and two times.

## Consequences

- **Only controllers that reached this service are listed.** An operator garam holds but that never contacted this service is absent, not listed with an empty entry. The console joins on `grn` against the operators garam lists, so an absent entry reads as never contacted.
- **A manager whose images change reports again only when it restarts.** Every image source is a flag or its own Pod spec, and either changes only through a new Pod.
- **`lastContactAt` lags the true last request by up to a minute,** per instance of the service.
- **A database a development build before ADR 0069 migrated to version 2 is not refused.** ADR 0069 has such a database recreated empty, and refused it as newer than migration 1. This binary takes version 2 as its own, so that database starts without `operators`. Controllers are still served, every contact write fails and is logged, and the console's read fails until the database is recreated.
- **The manager needs `POD_NAME`.** It is set in `config/manager/manager.yaml`. The manager already holds `get` on Pods.

## Rejected alternatives

- **Coalesce in SQL, with a conditional update per request.** It is still a statement per request, and the issue asks for no write on every request.
- **A flag naming the manager's own image.** kustomize's `images` transformer sets the container's image and does not reach an argument, so the flag would drift from what runs. It would also give no digest of what is running.
- **Report on every feed poll.** Images change only with a new Pod, so a report per poll would make `reportedAt` a second contact time and add one write per poll.
- **A route per operator, answering 404 for another organization's.** The agreed answer is one list per organization. The list leaves out other organizations' operators, which keeps the rule every read follows: another organization's object is answered as one nobody holds.
