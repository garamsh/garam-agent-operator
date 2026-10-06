package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrSchemaNotCurrent is returned for a database whose schema is not the newest this binary knows:
// not migrated yet, migrated by a newer binary, or left dirty by a failed migration. A command
// that writes without migrating checks for it first, because migrating is the server's alone
// (ADR 0056).
var ErrSchemaNotCurrent = errors.New("the database's schema is not the one this binary knows")

// undefinedTable is PostgreSQL's code for a relation that does not exist.
const undefinedTable = "42P01"

// readSchemaVersion is the version golang-migrate recorded, and whether it was left dirty.
const readSchemaVersion = `SELECT version, dirty FROM ` + migrationsTable + ` LIMIT 1`

// schemaRecord is what a database records of its schema: whether a version is recorded at all,
// which, and whether a migration to it failed.
type schemaRecord struct {
	recorded bool
	version  uint
	dirty    bool
}

// RequireCurrentSchema is ErrSchemaNotCurrent, saying what to do about it, unless the database is
// at the newest schema version this binary embeds and clean. It reads and changes nothing else.
func (p *Postgres) RequireCurrentSchema(ctx context.Context) error {
	known, err := latestMigration()
	if err != nil {
		return err
	}
	return p.requireSchema(ctx, known)
}

// requireSchema refuses a database whose recorded schema is not known.
func (p *Postgres) requireSchema(ctx context.Context, known uint) error {
	var record schemaRecord
	var version int64
	err := p.pool.QueryRow(ctx, readSchemaVersion).Scan(&version, &record.dirty)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pgErr) && pgErr.Code == undefinedTable:
	case err != nil:
		return fmt.Errorf("read the database's schema version: %v", err)
	default:
		record.recorded, record.version = true, uint(version)
	}
	return schemaVerdict(record, known)
}

// schemaVerdict is ErrSchemaNotCurrent, with what to do about it, unless record is known's.
func schemaVerdict(record schemaRecord, known uint) error {
	switch {
	case !record.recorded:
		return fmt.Errorf("%w: the database records no schema version and this binary needs %d: the control "+
			"service has not migrated it yet; retry after its rollout", ErrSchemaNotCurrent, known)
	case record.dirty:
		return fmt.Errorf("%w: the database is dirty at schema version %d: a migration failed there; the "+
			"control service's log says what to do", ErrSchemaNotCurrent, record.version)
	case record.version < known:
		return fmt.Errorf("%w: the database is at schema version %d and this binary needs %d: the control "+
			"service has not migrated it yet; retry after its rollout", ErrSchemaNotCurrent, record.version, known)
	case record.version > known:
		return fmt.Errorf("%w: the database is at schema version %d and this binary knows up to %d: a newer "+
			"control service migrated it; run the publish-profile of that version's image",
			ErrSchemaNotCurrent, record.version, known)
	}
	return nil
}
