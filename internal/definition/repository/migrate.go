package repository

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/stdlib"
)

// migrations are the schema's forward-only migrations, NNNNNN_<name>.up.sql applied in order, as
// garam numbers its own (garam@59fe68d migrations/). A schema change is a new file, never an edit.
//
//go:embed migrations/*.up.sql
var migrations embed.FS

// migrationsTable is where golang-migrate records the version a database is at.
const migrationsTable = "schema_migrations"

// migrateLock is the advisory lock Migrate holds from reading the schema to its last migration, so
// two binaries starting on one database never both decide what it holds. golang-migrate's own lock
// covers only each of its calls.
const migrateLock = "SELECT pg_advisory_lock(hashtext('garam-agent-operator control migrate'))"

// errSchemaRefused begins the error returned for a database the binary does not migrate: one a newer binary
// migrated, one a failed migration left dirty, or one holding tables with no migration recorded.
// Nothing in it is touched.
var errSchemaRefused = errors.New("database schema refused")

// Migrate brings the database to the newest schema this binary knows, under PostgreSQL's advisory
// lock, each migration in a transaction. A database with no migrations recorded is migrated from
// the first when it is empty, and refused otherwise (ADR 0069).
func (p *Postgres) Migrate(ctx context.Context, logger *slog.Logger) error {
	latest, err := latestMigration()
	if err != nil {
		return err
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrations: %v", err)
	}
	// Closing the connection releases the lock, as the session ends.
	defer func() { _ = conn.Conn().Close(context.Background()); conn.Release() }()
	if _, err := conn.Exec(ctx, migrateLock); err != nil {
		return fmt.Errorf("take the migration lock: %v", err)
	}
	if err := p.requireRecordedOrEmpty(ctx); err != nil {
		return err
	}

	db := stdlib.OpenDBFromPool(p.pool)
	defer func() { _ = db.Close() }()
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{MigrationsTable: migrationsTable})
	if err != nil {
		return fmt.Errorf("migrations: %v", err)
	}
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("migrations: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("migrations: %v", err)
	}
	// The driver holds a connection of the pool until closed, and the pool's Close waits for it.
	defer func() { _, _ = m.Close() }()
	from, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		from, err = 0, nil
	}
	if err != nil {
		return fmt.Errorf("read the database's schema version: %v", err)
	}
	switch {
	case dirty:
		return dirtyRefusal(from, "on an earlier start, whose log names the cause")
	case from > latest:
		return fmt.Errorf("%w: the database is at schema version %d and this binary knows up to %d: a newer "+
			"control service migrated it. Run that version or a later one; this one changes nothing",
			errSchemaRefused, from, latest)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		// golang-migrate's error carries the whole migration file; its cause is the database's own.
		cause := err
		var failed database.Error
		if errors.As(err, &failed) && failed.OrigErr != nil {
			cause = failed.OrigErr
		}
		if version, dirty, verr := m.Version(); verr == nil && dirty {
			return dirtyRefusal(version, "with "+cause.Error())
		}
		return fmt.Errorf("migrate the schema from version %d: %v", from, cause)
	}
	logger.Info("store schema migrated", "from", from, "to", latest)
	return nil
}

// dirtyRefusal is the error for a database dirty at version, stating the operator's step: a failed
// migration's transaction kept nothing, so the schema is the one before it.
func dirtyRefusal(version uint, cause string) error {
	return fmt.Errorf("%w: schema migration %d failed %s, and its transaction kept nothing, so the schema is "+
		"still the one before it; the database is recorded dirty at version %d. Correct or remove by hand the "+
		"rows the cause names, then clear the flag with psql \"$CONTROL_DATABASE_URL\" -c '%s' and start "+
		"again", errSchemaRefused, version, cause, version, clearDirty(version))
}

// clearDirty is the statement that records a database left dirty at version as the one before it,
// which is what a failed migration's rolled-back transaction leaves.
func clearDirty(version uint) string {
	if version <= 1 {
		return "DELETE FROM " + migrationsTable
	}
	return fmt.Sprintf("UPDATE %s SET version = %d, dirty = false", migrationsTable, version-1)
}

// unrecordedTables counts the current schema's tables other than the migrations table.
const unrecordedTables = `SELECT count(*) FROM pg_class
WHERE relnamespace = current_schema()::regnamespace AND relkind = 'r' AND relname <> $1`

// requireRecordedOrEmpty refuses a database with no migrations recorded that holds any table: a
// build before migrations, or something else, made it, and this binary migrates only from empty.
func (p *Postgres) requireRecordedOrEmpty(ctx context.Context) error {
	var recorded bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, migrationsTable).Scan(&recorded); err != nil {
		return fmt.Errorf("read the database's schema: %v", err)
	}
	if recorded {
		return nil
	}
	var tables int
	if err := p.pool.QueryRow(ctx, unrecordedTables, migrationsTable).Scan(&tables); err != nil {
		return fmt.Errorf("read the database's schema: %v", err)
	}
	if tables > 0 {
		return fmt.Errorf("%w: the database has no %s table and holds %d other tables, so a build before "+
			"migrations or something else made it, and this binary migrates only an empty database. Nothing "+
			"was changed. Recreate it empty", errSchemaRefused, migrationsTable, tables)
	}
	return nil
}

// migrationFile is one embedded migration and the version its name numbers.
type migrationFile struct {
	version uint
	name    string
}

func migrationFiles() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %v", err)
	}
	var files []migrationFile
	for _, e := range entries {
		number, _, ok := strings.Cut(e.Name(), "_")
		version, err := strconv.ParseUint(number, 10, 64)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s is not named NNNNNN_<name>.up.sql", e.Name())
		}
		files = append(files, migrationFile{version: uint(version), name: e.Name()})
	}
	slices.SortFunc(files, func(a, b migrationFile) int { return int(a.version) - int(b.version) })
	return files, nil
}

func latestMigration() (uint, error) {
	files, err := migrationFiles()
	if err != nil || len(files) == 0 {
		return 0, fmt.Errorf("no migration embedded: %v", err)
	}
	return files[len(files)-1].version, nil
}
