-- Migration 1: the schema the published control service 7c216469476d created, verbatim
-- (git show 7c21646:internal/definition/repository/schema.sql, below its first line). A database
-- holding exactly this and no schema_migrations table is adopted at this version (ADR 0054).

CREATE TABLE IF NOT EXISTS profiles (
    name     text   NOT NULL,
    version  bigint NOT NULL CHECK (version >= 1),
    settings jsonb  NOT NULL,
    PRIMARY KEY (name, version)
);

CREATE TABLE IF NOT EXISTS templates (
    name            text   NOT NULL,
    version         bigint NOT NULL CHECK (version >= 1),
    profile_name    text   NOT NULL,
    profile_version bigint NOT NULL,
    config          jsonb  NOT NULL,
    PRIMARY KEY (name, version),
    FOREIGN KEY (profile_name, profile_version) REFERENCES profiles (name, version)
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
    revision            bigint NOT NULL CHECK (revision >= 1),
    profile_name        text   NOT NULL,
    profile_version     bigint NOT NULL,
    config              jsonb  NOT NULL,
    position            bigint NOT NULL UNIQUE,
    assignment_operator text,
    assignment_epoch    text,
    PRIMARY KEY (agent, revision),
    FOREIGN KEY (profile_name, profile_version) REFERENCES profiles (name, version),
    CHECK ((assignment_operator IS NULL) = (assignment_epoch IS NULL))
);

CREATE TABLE IF NOT EXISTS creations (
    actor            text   NOT NULL,
    organization     text   NOT NULL,
    request_id       text   NOT NULL,
    template_name    text   NOT NULL,
    template_version bigint NOT NULL,
    state            text   NOT NULL CHECK (state IN ('pending', 'registered', 'failed')),
    agent            text,
    reason           text,
    PRIMARY KEY (organization, request_id),
    FOREIGN KEY (template_name, template_version) REFERENCES templates (name, version),
    CHECK ((state = 'registered') = (agent IS NOT NULL)),
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
    agent             text   PRIMARY KEY,
    observed_revision bigint NOT NULL CHECK (observed_revision >= 1),
    rendered_revision bigint NOT NULL CHECK (rendered_revision >= 1),
    applied_revision  bigint
);
