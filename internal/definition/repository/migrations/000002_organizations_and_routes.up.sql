-- Migration 2: from 7c216469476d's schema (migration 1) to the organization-scoped schema with the
-- console's publication and the controller and agent routes' tables (#270, #220, #212, #217, #218).
-- The whole file runs in one transaction. It drops no row the earlier schema stored and truncates
-- no table (ADR 0055):
--   - an organization the earlier rows lack is read from what they carry: a definition's from its
--     agent's GRN, grn:<organization>:..., a template's from the creations naming it, a profile's
--     from the templates and definitions naming it, one copy per organization naming it;
--   - a row with no such source moves unchanged into an archive nothing reads: every creation, whose
--     bound fields, controller and epoch the earlier schema never stored, into creations_n1, and a
--     template or profile nothing names into templates_n1 or profiles_n1. An archive is created only
--     where a row moves into it, so this migration over an empty database yields exactly the schema
--     a fresh database has, which is what adopts one made before migrations at this version;
--   - before an earlier table is dropped, every one of its rows is found in its successor or its
--     archive, or the migration fails and nothing of it is kept.

-- A definition whose agent's GRN names no organization has no source for it, and is never archived:
-- it is an agent's desired state. Refuse, keeping everything as it was.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM definitions WHERE split_part(agent, ':', 2) = '') THEN
        RAISE EXCEPTION 'definitions hold an agent whose GRN names no organization: %',
            (SELECT string_agg(DISTINCT agent, ', ') FROM definitions WHERE split_part(agent, ':', 2) = '');
    END IF;
END $$;

-- The earlier tables step aside under their own names, each primary key's index with them, so the
-- successors take the names a fresh database gives them.
ALTER TABLE creations RENAME TO creations_v1;
ALTER INDEX creations_pkey RENAME TO creations_v1_pkey;
ALTER TABLE templates RENAME TO templates_v1;
ALTER INDEX templates_pkey RENAME TO templates_v1_pkey;
ALTER TABLE definitions RENAME TO definitions_v1;
ALTER INDEX definitions_pkey RENAME TO definitions_v1_pkey;
ALTER INDEX definitions_position_key RENAME TO definitions_v1_position_key;
ALTER TABLE profiles RENAME TO profiles_v1;
ALTER INDEX profiles_pkey RENAME TO profiles_v1_pkey;
ALTER TABLE requests DROP CONSTRAINT requests_agent_revision_fkey;

-- Who names what: a template is its creations' organizations', a profile its templates' and
-- definitions'.
CREATE TEMPORARY TABLE template_owners ON COMMIT DROP AS
    SELECT DISTINCT organization, template_name AS name, template_version AS version FROM creations_v1;
CREATE TEMPORARY TABLE profile_owners ON COMMIT DROP AS
    SELECT o.organization, t.profile_name AS name, t.profile_version AS version
    FROM template_owners o JOIN templates_v1 t ON t.name = o.name AND t.version = o.version
    UNION
    SELECT split_part(agent, ':', 2), profile_name, profile_version FROM definitions_v1;
-- What moved into an archive, which the final check accounts for.
CREATE TEMPORARY TABLE moved (kind text NOT NULL, name text NOT NULL, version bigint NOT NULL) ON COMMIT DROP;

CREATE TABLE profiles (
    organization text   NOT NULL,
    name         text   NOT NULL,
    version      bigint NOT NULL CHECK (version >= 1),
    settings     jsonb  NOT NULL,
    PRIMARY KEY (organization, name, version)
);

INSERT INTO profiles (organization, name, version, settings)
    SELECT o.organization, p.name, p.version, p.settings
    FROM profiles_v1 p JOIN profile_owners o ON o.name = p.name AND o.version = p.version;
-- An archive keeps every earlier column, and when and by which migration the row moved.
DO $$
BEGIN
    INSERT INTO moved SELECT 'profile', p.name, p.version FROM profiles_v1 p
        WHERE NOT EXISTS (SELECT 1 FROM profile_owners o WHERE o.name = p.name AND o.version = p.version);
    IF FOUND THEN
        CREATE TABLE profiles_n1 (
            name             text        NOT NULL,
            version          bigint      NOT NULL,
            settings         jsonb       NOT NULL,
            migrated_version bigint      NOT NULL,
            migrated_at      timestamptz NOT NULL,
            PRIMARY KEY (name, version)
        );
        INSERT INTO profiles_n1 (name, version, settings, migrated_version, migrated_at)
            SELECT p.name, p.version, p.settings, 2, now() FROM profiles_v1 p
            JOIN moved m ON m.kind = 'profile' AND m.name = p.name AND m.version = p.version;
    END IF;
END $$;

CREATE TABLE templates (
    organization    text   NOT NULL,
    name            text   NOT NULL,
    version         bigint NOT NULL CHECK (version >= 1),
    profile_name    text   NOT NULL,
    profile_version bigint NOT NULL,
    config          jsonb  NOT NULL,
    PRIMARY KEY (organization, name, version),
    FOREIGN KEY (organization, profile_name, profile_version) REFERENCES profiles (organization, name, version)
);

INSERT INTO templates (organization, name, version, profile_name, profile_version, config)
    SELECT o.organization, t.name, t.version, t.profile_name, t.profile_version, t.config
    FROM templates_v1 t JOIN template_owners o ON o.name = t.name AND o.version = t.version;
DO $$
BEGIN
    INSERT INTO moved SELECT 'template', t.name, t.version FROM templates_v1 t
        WHERE NOT EXISTS (SELECT 1 FROM template_owners o WHERE o.name = t.name AND o.version = t.version);
    IF FOUND THEN
        CREATE TABLE templates_n1 (
            name             text        NOT NULL,
            version          bigint      NOT NULL,
            profile_name     text        NOT NULL,
            profile_version  bigint      NOT NULL,
            config           jsonb       NOT NULL,
            migrated_version bigint      NOT NULL,
            migrated_at      timestamptz NOT NULL,
            PRIMARY KEY (name, version)
        );
        INSERT INTO templates_n1 (name, version, profile_name, profile_version, config, migrated_version,
            migrated_at)
            SELECT t.name, t.version, t.profile_name, t.profile_version, t.config, 2, now() FROM templates_v1 t
            JOIN moved m ON m.kind = 'template' AND m.name = t.name AND m.version = t.version;
    END IF;
END $$;
CREATE TABLE publications (
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

CREATE TABLE definitions (
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

INSERT INTO definitions (agent, organization, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
    SELECT agent, split_part(agent, ':', 2), revision, profile_name, profile_version, config, position,
        assignment_operator, assignment_epoch
    FROM definitions_v1;
ALTER TABLE requests ADD FOREIGN KEY (agent, revision) REFERENCES definitions (agent, revision);

CREATE TABLE creations (
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

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM creations_v1) THEN
        CREATE TABLE creations_n1 (
            actor            text        NOT NULL,
            organization     text        NOT NULL,
            request_id       text        NOT NULL,
            template_name    text        NOT NULL,
            template_version bigint      NOT NULL,
            state            text        NOT NULL,
            agent            text,
            reason           text,
            migrated_version bigint      NOT NULL,
            migrated_at      timestamptz NOT NULL,
            PRIMARY KEY (organization, request_id)
        );
        INSERT INTO creations_n1 (actor, organization, request_id, template_name, template_version, state,
            agent, reason, migrated_version, migrated_at)
            SELECT actor, organization, request_id, template_name, template_version, state, agent, reason, 2,
                now()
            FROM creations_v1;
        INSERT INTO moved SELECT 'creation', organization || '/' || request_id, 0 FROM creations_v1;
    END IF;
END $$;

ALTER TABLE agent_status
    ADD COLUMN applied_activation_id text,
    ADD COLUMN applied_generation    text,
    ADD COLUMN applied_observed_at   timestamptz,
    ADD CHECK ((applied_revision IS NULL) = (applied_activation_id IS NULL)),
    ADD CHECK ((applied_revision IS NULL) = (applied_generation IS NULL)),
    ADD CHECK ((applied_revision IS NULL) = (applied_observed_at IS NULL));

-- Every earlier row is in its successor or its archive, or nothing of this migration is kept.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM profiles_v1 p
               WHERE NOT EXISTS (SELECT 1 FROM profiles n
                                 WHERE n.name = p.name AND n.version = p.version AND n.settings = p.settings)
                 AND NOT EXISTS (SELECT 1 FROM moved m
                                 WHERE m.kind = 'profile' AND m.name = p.name AND m.version = p.version))
       OR EXISTS (SELECT 1 FROM templates_v1 t
                  WHERE NOT EXISTS (SELECT 1 FROM templates n
                                    WHERE n.name = t.name AND n.version = t.version AND n.config = t.config)
                    AND NOT EXISTS (SELECT 1 FROM moved m
                                    WHERE m.kind = 'template' AND m.name = t.name AND m.version = t.version))
       OR (SELECT count(*) FROM definitions_v1) <> (SELECT count(*) FROM definitions)
       OR (SELECT count(*) FROM creations_v1) <> (SELECT count(*) FROM moved WHERE kind = 'creation') THEN
        RAISE EXCEPTION 'an earlier row has neither a successor nor an archive';
    END IF;
END $$;

DROP TABLE creations_v1;
DROP TABLE templates_v1;
DROP TABLE definitions_v1;
DROP TABLE profiles_v1;

-- A creation's agent names one creation, which a first-certificate request is sent under.
CREATE UNIQUE INDEX creations_agent ON creations (agent);

-- One first-certificate request per agent, as its controller sent it, and the public result garam
-- answered. A row is pending while the outcome is unknown; a refusal removes it.
CREATE TABLE initial_certificates (
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
CREATE TABLE placements (
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
CREATE UNIQUE INDEX placements_current ON placements (agent) WHERE revoked_at IS NULL;

-- Every activation request an agent's adapter made, under the identifier the adapter derives, with
-- what control sends garam for it. The anchor and the reference are fixed when the row is
-- inserted, so every attempt sends garam the same request; activation_id is null until garam
-- answers. Tokens are never stored.
CREATE TABLE activation_requests (
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
CREATE TABLE agent_activations (
    agent                text PRIMARY KEY,
    latest_activation_id text NOT NULL
);

-- Each legacy agent's cutover import (garam ADR-0086): its source as read from garam, every value
-- verbatim with its disposition, and the digest garam's freeze verifies. Its revision 1 is stored
-- inactive, with no assignment, until the import is switched.
CREATE TABLE cutover_imports (
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
