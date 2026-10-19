-- Migration 3: drop the legacy agents' cutover imports. garam v0.5.0 serves no cutover, so no
-- import is read or recorded again (ADR 0077).
DROP TABLE cutover_imports;
