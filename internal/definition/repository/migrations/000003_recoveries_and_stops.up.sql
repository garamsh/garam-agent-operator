-- Migration 3: an agent's credential recoveries and its stops without a replacement (#280,
-- ADR 0057). It adds two tables and changes nothing stored.

-- Every credential recovery of an agent: the console request that opened it under an
-- agent:recover authority of its own request identifier, the epoch it was opened under, and, once
-- the agent's controller prepared its certificate request, the exact bytes garam is sent, over
-- which the finalizing handoff is minted. garam's answer is kept once it is finalized. One
-- recovery per agent is open.
CREATE TABLE recoveries (
    agent               text   NOT NULL,
    recovery_request_id text   NOT NULL,
    organization        text   NOT NULL,
    request_id          text   NOT NULL,
    actor               text   NOT NULL,
    operation           text   NOT NULL,
    target              text   NOT NULL,
    body_sha256         text   NOT NULL,
    operation_ref       text   NOT NULL,
    assignment_operator text   NOT NULL,
    assignment_epoch    text   NOT NULL,
    epoch               text   NOT NULL,
    stage               text   NOT NULL CHECK (stage IN ('requested', 'prepared', 'finalized')),
    garam_body          bytea,
    garam_body_sha256   text,
    lineage             text,
    certificate_pem     text,
    opened_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent, recovery_request_id),
    UNIQUE (organization, request_id),
    CHECK ((stage = 'requested') = (garam_body IS NULL)),
    CHECK ((garam_body IS NULL) = (garam_body_sha256 IS NULL)),
    CHECK ((stage = 'finalized') = (lineage IS NOT NULL AND certificate_pem IS NOT NULL))
);

CREATE UNIQUE INDEX recoveries_open ON recoveries (agent) WHERE stage <> 'finalized';

-- Every stop of an agent without a replacement: the console request that made it under
-- agent:configure, the agent's latest activation when it was recorded, and when garam answered
-- that activation's deactivation. A stop is current until a start ends it; the start's request
-- is recorded on it, and the row is kept.
CREATE TABLE stops (
    organization              text NOT NULL,
    request_id                text NOT NULL,
    agent                     text NOT NULL,
    actor                     text NOT NULL,
    operation                 text NOT NULL,
    target                    text NOT NULL,
    body_sha256               text NOT NULL,
    operation_ref             text NOT NULL,
    assignment_operator       text NOT NULL,
    assignment_epoch          text NOT NULL,
    activation_id             text,
    stopped_at                timestamptz NOT NULL DEFAULT now(),
    deactivated_at            timestamptz,
    start_request_id          text,
    start_actor               text,
    start_operation           text,
    start_target              text,
    start_body_sha256         text,
    start_operation_ref       text,
    start_assignment_operator text,
    start_assignment_epoch    text,
    started_at                timestamptz,
    PRIMARY KEY (organization, request_id),
    UNIQUE (organization, start_request_id),
    CHECK ((started_at IS NULL) = (start_request_id IS NULL)),
    CHECK ((start_request_id IS NULL) = (start_actor IS NULL AND start_operation IS NULL
        AND start_target IS NULL AND start_body_sha256 IS NULL AND start_operation_ref IS NULL
        AND start_assignment_operator IS NULL AND start_assignment_epoch IS NULL))
);

-- One current stop per agent.
CREATE UNIQUE INDEX stops_current ON stops (agent) WHERE started_at IS NULL;
