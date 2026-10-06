-- The control service's desired state. Every statement is idempotent: the binary applies this at every start.

-- A profile's and a template's names and versions are their organization's own. Every key naming
-- one carries the organization, so nothing refers to another organization's.
CREATE TABLE IF NOT EXISTS profiles (
    organization text   NOT NULL,
    name         text   NOT NULL,
    version      bigint NOT NULL CHECK (version >= 1),
    settings     jsonb  NOT NULL,
    PRIMARY KEY (organization, name, version)
);

CREATE TABLE IF NOT EXISTS templates (
    organization    text   NOT NULL,
    name            text   NOT NULL,
    version         bigint NOT NULL CHECK (version >= 1),
    profile_name    text   NOT NULL,
    profile_version bigint NOT NULL,
    config          jsonb  NOT NULL,
    PRIMARY KEY (organization, name, version),
    FOREIGN KEY (organization, profile_name, profile_version) REFERENCES profiles (organization, name, version)
);
-- One row per publish request, keyed as configure's requests are, with the version it published,
-- which every repeat of the key returns.
CREATE TABLE IF NOT EXISTS publications (
    organization     text   NOT NULL,
    request_id       text   NOT NULL,
    actor            text   NOT NULL,
    operation        text   NOT NULL,
    target           text   NOT NULL,
    body_sha256      text   NOT NULL,
    operation_ref    text   NOT NULL,
    template_name    text   NOT NULL,
    template_version bigint NOT NULL,
    PRIMARY KEY (organization, request_id),
    FOREIGN KEY (organization, template_name, template_version) REFERENCES templates (organization, name, version)
);

-- The one row holding the position the latest stored revision took. Every writer of a revision
-- takes the next position by updating it, so writers serialize on it and positions are taken
-- in the order revisions commit: a reader holding a position has seen every revision below it.
CREATE TABLE IF NOT EXISTS positions (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    position  bigint  NOT NULL
);
INSERT INTO positions (singleton, position) VALUES (true, 0) ON CONFLICT DO NOTHING;

-- The primary key is what refuses a second revision under one number, so two
-- updates based on one revision cannot both be stored.
CREATE TABLE IF NOT EXISTS definitions (
    agent               text   NOT NULL,
    organization        text   NOT NULL,
    revision            bigint NOT NULL CHECK (revision >= 1),
    profile_name        text   NOT NULL,
    profile_version     bigint NOT NULL,
    config              jsonb  NOT NULL,
    position            bigint NOT NULL UNIQUE,
    assignment_operator text,
    assignment_epoch    text,
    PRIMARY KEY (agent, revision),
    FOREIGN KEY (organization, profile_name, profile_version) REFERENCES profiles (organization, name, version),
    CHECK ((assignment_operator IS NULL) = (assignment_epoch IS NULL))
);

-- One row per create request, keyed as garam keys the operation's request id. The binding
-- columns are compared on a repeat, not interpreted.
CREATE TABLE IF NOT EXISTS creations (
    organization     text    NOT NULL,
    request_id       text    NOT NULL,
    actor            text    NOT NULL,
    operation        text    NOT NULL,
    target           text    NOT NULL,
    body_sha256      text    NOT NULL,
    operation_ref    text    NOT NULL,
    controller       text    NOT NULL,
    template_name    text    NOT NULL,
    template_version bigint  NOT NULL,
    profile_name     text    NOT NULL,
    profile_version  bigint  NOT NULL,
    state            text    NOT NULL CHECK (state IN ('pending', 'registered', 'failed')),
    agent            text,
    epoch            text,
    reason           text,
    conflict         boolean NOT NULL DEFAULT false,
    PRIMARY KEY (organization, request_id),
    FOREIGN KEY (organization, template_name, template_version) REFERENCES templates (organization, name, version),
    FOREIGN KEY (organization, profile_name, profile_version) REFERENCES profiles (organization, name, version),
    CHECK ((state = 'registered') = (agent IS NOT NULL AND epoch IS NOT NULL)),
    CHECK ((state = 'failed') = (reason IS NOT NULL))
);

-- One row per configure request, keyed as garam keys the operation's request id. A repeat
-- returns the outcome stored here; the binding columns are compared, not interpreted.
CREATE TABLE IF NOT EXISTS requests (
    organization        text   NOT NULL,
    request_id          text   NOT NULL,
    actor               text   NOT NULL,
    operation           text   NOT NULL,
    target              text   NOT NULL,
    body_sha256         text   NOT NULL,
    operation_ref       text   NOT NULL,
    assignment_operator text   NOT NULL,
    assignment_epoch    text   NOT NULL,
    agent               text   NOT NULL,
    outcome             text   NOT NULL CHECK (outcome IN ('applied', 'stale')),
    revision            bigint,
    PRIMARY KEY (organization, request_id),
    FOREIGN KEY (agent, revision) REFERENCES definitions (agent, revision),
    CHECK ((outcome = 'applied') = (revision IS NOT NULL))
);

-- What controllers reported of each agent. A report raises a field and never lowers it;
-- applied_revision is set by the runtime's own report, never by a controller's.
CREATE TABLE IF NOT EXISTS agent_status (
    agent                 text   PRIMARY KEY,
    observed_revision     bigint NOT NULL CHECK (observed_revision >= 1),
    rendered_revision     bigint NOT NULL CHECK (rendered_revision >= 1),
    applied_revision      bigint,
    applied_activation_id text,
    CHECK ((applied_revision IS NULL) = (applied_activation_id IS NULL))
);

-- A creation's agent names one creation, which a first-certificate request is sent under.
CREATE UNIQUE INDEX IF NOT EXISTS creations_agent ON creations (agent);

-- One first-certificate request per agent, as its controller sent it, and the public result garam
-- answered. A row is pending while the outcome is unknown; a refusal removes it.
CREATE TABLE IF NOT EXISTS initial_certificates (
    agent                   text PRIMARY KEY REFERENCES creations (agent),
    request_id              text NOT NULL,
    epoch                   text NOT NULL,
    certificate_request_pem text NOT NULL,
    state                   text NOT NULL CHECK (state IN ('pending', 'issued')),
    certificate_pem         text,
    issuer_pem              text,
    server_root_pem         text,
    not_after               timestamptz,
    CHECK ((state = 'issued') = (certificate_pem IS NOT NULL AND issuer_pem IS NOT NULL
        AND server_root_pem IS NOT NULL AND not_after IS NOT NULL))
);

-- Every placement a controller registered for an agent: the Pod, the state claim it started on,
-- the epoch, the digest of its placement token, the placement it replaced with the digest of that
-- one's writer-stopped evidence, and the controller's leaf exactly as presented. One per agent is
-- current; a replaced one is revoked and kept, so it is never registered again.
CREATE TABLE IF NOT EXISTS placements (
    agent                          text        NOT NULL,
    pod_uid                        text        NOT NULL,
    controller                     text        NOT NULL,
    epoch                          text        NOT NULL,
    pvc_uid                        text        NOT NULL,
    token_sha256                   text        NOT NULL,
    previous_pod_uid               text        NOT NULL DEFAULT '',
    previous_writer_stopped_sha256 text        NOT NULL DEFAULT '',
    leaf_der                       bytea       NOT NULL,
    registered_at                  timestamptz NOT NULL DEFAULT now(),
    revoked_at                     timestamptz,
    PRIMARY KEY (agent, pod_uid)
);

-- One current placement per agent: the one it replaces is revoked before it can be stored.
CREATE UNIQUE INDEX IF NOT EXISTS placements_current ON placements (agent) WHERE revoked_at IS NULL;

-- Every activation request an agent's adapter made, under the identifier the adapter derives, with
-- what control sends garam for it. The anchor and the reference are fixed when the row is
-- inserted, so every attempt sends garam the same request; activation_id is null until garam
-- answers. Tokens are never stored.
CREATE TABLE IF NOT EXISTS activation_requests (
    agent                  text   NOT NULL,
    request_id             text   NOT NULL,
    epoch                  text   NOT NULL,
    generation             text   NOT NULL,
    config_revision        bigint NOT NULL CHECK (config_revision >= 1),
    placement_pod_uid      text   NOT NULL,
    replaces_activation_id text   NOT NULL DEFAULT '',
    operation_ref          text   NOT NULL DEFAULT '',
    activation_id          text,
    PRIMARY KEY (agent, request_id)
);

-- Each agent's most recent activation, current or ended: the next activation's anchor.
CREATE TABLE IF NOT EXISTS agent_activations (
    agent                text PRIMARY KEY,
    latest_activation_id text NOT NULL
);

-- Each legacy agent's cutover import (garam ADR-0086): its source as read from garam, every value
-- verbatim with its disposition, and the digest garam's freeze verifies. Its revision 1 is stored
-- inactive, with no assignment, until the import is switched.
CREATE TABLE IF NOT EXISTS cutover_imports (
    agent           text   PRIMARY KEY,
    organization    text   NOT NULL,
    import_id       text   NOT NULL,
    epoch           text   NOT NULL,
    assignee        text   NOT NULL,
    source_digest   text   NOT NULL,
    source_values   jsonb  NOT NULL,
    dispositions    jsonb  NOT NULL,
    profile_name    text   NOT NULL,
    profile_version bigint NOT NULL,
    pins            jsonb  NOT NULL,
    stage           text   NOT NULL CHECK (stage IN ('imported', 'frozen', 'switched')),
    configure_ref   text   NOT NULL DEFAULT '',
    CHECK ((stage = 'switched') = (configure_ref <> ''))
);
