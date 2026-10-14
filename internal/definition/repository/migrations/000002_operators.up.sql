-- Migration 2: what each controller reports of itself, and when it last reached this service
-- (ADR 0074). A row exists once the controller made an authenticated request.
CREATE TABLE operators (
    grn             text        PRIMARY KEY,
    org_grn         text        NOT NULL,
    images          jsonb,
    reported_at     timestamptz,
    last_contact_at timestamptz,
    CHECK ((images IS NULL) = (reported_at IS NULL))
);
CREATE INDEX operators_org_grn ON operators (org_grn);
