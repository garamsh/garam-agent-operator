-- The control service's desired state. Every statement is idempotent: the binary applies this at every start.

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

-- The primary key is what refuses a second revision under one number, so two
-- updates based on one revision cannot both be stored.
CREATE TABLE IF NOT EXISTS definitions (
    agent           text   NOT NULL,
    revision        bigint NOT NULL CHECK (revision >= 1),
    profile_name    text   NOT NULL,
    profile_version bigint NOT NULL,
    config          jsonb  NOT NULL,
    PRIMARY KEY (agent, revision),
    FOREIGN KEY (profile_name, profile_version) REFERENCES profiles (name, version)
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
    PRIMARY KEY (actor, organization, request_id),
    FOREIGN KEY (template_name, template_version) REFERENCES templates (name, version),
    CHECK ((state = 'registered') = (agent IS NOT NULL)),
    CHECK ((state = 'failed') = (reason IS NOT NULL))
);
