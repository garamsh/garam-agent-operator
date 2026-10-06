package repository

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// migrations are the schema's forward-only migrations, NNNNNN_<name>.up.sql applied in order, as
// garam numbers its own (garam@59fe68d migrations/). A schema change is a new file, never an edit.
//
//go:embed migrations/*.up.sql
var migrations embed.FS

// adoptable are the versions a database made before migrations existed is adopted at, when its
// schema is exactly what migrations up to that version produce (ADR 0054): 1, what the published
// 7c216469476d created, and 2, what the last schema.sql before migrations created.
var adoptable = []uint{1, 2}

// migrationsTable is where golang-migrate records the version a database is at.
const migrationsTable = "schema_migrations"

// migrateLock is the advisory lock Migrate holds from reading the schema to its last migration, so
// two binaries starting on one database never both adopt it. golang-migrate's own lock covers only
// each of its calls.
const migrateLock = "SELECT pg_advisory_lock(hashtext('garam-agent-operator control migrate'))"

// errSchemaRefused begins the error returned for a database the binary does not migrate: one a newer binary
// migrated, one a failed migration left dirty, or one made before migrations whose schema is no
// version this binary knows. Nothing in it is touched.
var errSchemaRefused = errors.New("database schema refused")

// Migrate brings the database to the newest schema this binary knows, under PostgreSQL's advisory
// lock, each migration in a transaction. A database with no migrations recorded is migrated from
// the first when it is empty, adopted at a version whose schema it matches exactly, and refused
// otherwise. A row a migration moved into an archive is logged at WARN by its key.
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
	adopt, err := p.adoption(ctx)
	if err != nil {
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
	if adopt > 0 {
		if err := m.Force(int(adopt)); err != nil {
			return fmt.Errorf("adopt the database at version %d: %v", adopt, err)
		}
		logger.Info("database made before migrations adopted", "version", adopt)
	}

	from, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		from, err = 0, nil
	}
	if err != nil {
		return fmt.Errorf("read the database's schema version: %v", err)
	}
	switch {
	case dirty:
		return fmt.Errorf("%w: the database is dirty at version %d: a migration failed there and its "+
			"transaction left nothing, but the version was not recorded as done. Read that run's log, and once "+
			"the schema is known to be version %d's predecessor, force the recorded version back to %d and start "+
			"again", errSchemaRefused, from, from, from-1)
	case from > latest:
		return fmt.Errorf("%w: the database is at schema version %d and this binary knows up to %d: a newer "+
			"control service migrated it. Run that version or a later one; this one changes nothing",
			errSchemaRefused, from, latest)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate the schema from version %d: %v", from, err)
	}
	logger.Info("store schema migrated", "from", from, "to", latest)
	if from < 2 && latest >= 2 {
		return p.logArchived(ctx, logger)
	}
	return nil
}

// adoption is the version a database with no migrations recorded is adopted at: 0 where there is
// nothing to adopt, an empty database or one already migrating. A database whose schema matches no
// adoptable version is refused, naming what differs from the nearest.
func (p *Postgres) adoption(ctx context.Context) (uint, error) {
	var recorded bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, migrationsTable).Scan(&recorded); err != nil {
		return 0, fmt.Errorf("read the database's schema: %v", err)
	}
	if recorded {
		return 0, nil
	}
	found, err := signature(ctx, p.pool)
	if err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, nil
	}
	var nearest []string
	for _, version := range adoptable {
		expected, err := p.expectedSignature(ctx, version)
		if err != nil {
			return 0, err
		}
		differences := difference(expected, found)
		if len(differences) == 0 {
			return version, nil
		}
		if nearest == nil || len(differences) < len(nearest) {
			nearest = differences
		}
	}
	return 0, fmt.Errorf("%w: the database has no %s table and its schema is none this binary knows, so it "+
		"was made by a build this binary cannot migrate from. Nothing was changed. It differs from the "+
		"nearest known schema in: %s", errSchemaRefused, migrationsTable, strings.Join(nearest, "; "))
}

// expectedSignature is the signature of the schema migrations 1 to version produce, built in a
// scratch schema inside a transaction that is always rolled back.
func (p *Postgres) expectedSignature(ctx context.Context, version uint) ([]string, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("build schema version %d: %v", version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `CREATE SCHEMA control_expected; SET LOCAL search_path TO control_expected`); err != nil {
		return nil, fmt.Errorf("build schema version %d: %v", version, err)
	}
	files, err := migrationFiles()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.version > version {
			break
		}
		body, err := migrations.ReadFile("migrations/" + file.name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %v", file.name, err)
		}
		// The simple protocol runs a file of several statements, as golang-migrate does.
		if _, err := tx.Conn().PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
			return nil, fmt.Errorf("build schema version %d at %s: %v", version, file.name, err)
		}
	}
	return signature(ctx, tx)
}

// querier is a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// signatureQuery describes the current schema's tables through the catalog: each table, each column
// with its type and nullability, each primary key, foreign key, unique and check constraint by its
// definition, and each index by its definition with its name left out. The migrations table is not
// part of the schema it records.
const signatureQuery = `
WITH t AS (
    SELECT c.oid, c.relname FROM pg_class c
    WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind = 'r' AND c.relname <> $1
)
SELECT 'table ' || relname FROM t
UNION ALL
SELECT 'column ' || t.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod) ||
    CASE WHEN a.attnotnull THEN ' not null' ELSE ' null' END
FROM t JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT 'constraint ' || t.relname || ' ' || pg_get_constraintdef(k.oid)
FROM t JOIN pg_constraint k ON k.conrelid = t.oid AND k.contype IN ('p', 'f', 'u', 'c')
UNION ALL
SELECT 'index ' || t.relname || ' ' || pg_get_indexdef(i.indexrelid)
FROM t JOIN pg_index i ON i.indrelid = t.oid`

// indexName is the part of an index's definition that names it and its schema-qualified table, which
// the line already names unqualified.
var indexName = regexp.MustCompile(`INDEX \S+ ON \S+ USING `)

func signature(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, signatureQuery, migrationsTable)
	if err != nil {
		return nil, fmt.Errorf("read the database's schema: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("read the database's schema: %v", err)
		}
		out = append(out, indexName.ReplaceAllString(line, "INDEX USING "))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the database's schema: %v", err)
	}
	slices.Sort(out)
	return out, nil
}

// difference is what expected and found do not share: a line expected and missing, or found and
// not expected.
func difference(expected, found []string) []string {
	var out []string
	for _, line := range expected {
		if !slices.Contains(found, line) {
			out = append(out, "missing "+line)
		}
	}
	for _, line := range found {
		if !slices.Contains(expected, line) {
			out = append(out, "unexpected "+line)
		}
	}
	return out
}

// archives are the tables migration 2 moves earlier rows into, each with the columns its key is read
// from (ADR 0054).
var archives = []struct{ table, key string }{
	{"creations_n1", "organization || '/' || request_id"},
	{"templates_n1", "name || ' version ' || version"},
	{"profiles_n1", "name || ' version ' || version"},
}

// logArchived logs at WARN every row migration 2 moved into an archive, by its key.
func (p *Postgres) logArchived(ctx context.Context, logger *slog.Logger) error {
	for _, a := range archives {
		var exists bool
		if err := p.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, a.table).Scan(&exists); err != nil {
			return fmt.Errorf("read archive %s: %v", a.table, err)
		}
		if !exists {
			continue
		}
		rows, err := p.pool.Query(ctx, `SELECT `+a.key+` FROM `+a.table+` WHERE migrated_version = 2 ORDER BY 1`)
		if err != nil {
			return fmt.Errorf("read archive %s: %v", a.table, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read archive %s: %v", a.table, err)
		}
		for _, key := range keys {
			logger.Warn("row archived by schema migration 2: it had no source for a column the schema now requires",
				"archive", a.table, "key", key)
		}
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
