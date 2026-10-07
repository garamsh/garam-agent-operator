-- Migration 4: the issuer and garam server root a recovery's certificate was signed under, as
-- garam answers them from f54b9e8 on (garamsh/garam#1223, ADR-0091; ADR 0062). Both are null for a
-- recovery finalized before, or answered by a garam that names none. Nothing stored changes.
ALTER TABLE recoveries
    ADD COLUMN issuer_pem      text,
    ADD COLUMN server_root_pem text;
