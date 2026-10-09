//go:build e2e

package control_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latestVersion is the newest migration the binary embeds.
const latestVersion = 1

// migrationsTable is where the binary records the schema version.
const migrationsTable = "schema_migrations"

// schemaTables are the tables migration 1 creates.
var schemaTables = []string{
	"activation_requests", "agent_activations", "agent_status", "creations", "cutover_imports", "definitions",
	"initial_certificates", "placements", "positions", "profiles", "publications", "recoveries", "requests",
	"stops", "templates",
}

// newDatabase creates an empty database on the suite's server, dropped when the test ends, and
// returns its URL and a pool of the test's own on it.
func newDatabase(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := fmt.Sprintf("migration_%d_%d", runID, names.Add(1))
	_, err := pool.Exec(ctx, "CREATE DATABASE "+db)
	require.NoError(t, err)
	url := strings.Replace(databaseURL, "/control?", "/"+db+"?", 1)
	require.NotEqual(t, databaseURL, url)
	own, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() {
		own.Close()
		_, _ = pool.Exec(context.Background(), "DROP DATABASE "+db+" WITH (FORCE)")
	})
	return url, own
}

// startMigrating starts the binary on the database at url, stopped when the test ends, and returns
// the base URL of its console routes and its log so far.
func startMigrating(t *testing.T, url string) (string, *process) {
	t.Helper()
	p, base, err := startOn(strings.ReplaceAll(t.Name(), "/", "-"), url)
	require.NoError(t, err)
	t.Cleanup(p.stop)
	return base, p
}

// logOf is what p has logged so far.
func logOf(t *testing.T, p *process) string {
	t.Helper()
	raw, err := os.ReadFile(p.log)
	require.NoError(t, err)
	return string(raw)
}

// refusedOn runs the binary on the database at url, requires it to exit without becoming ready,
// and returns what it wrote.
func refusedOn(t *testing.T, url string) string {
	t.Helper()
	args, _ := withoutFlag(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Env = append(os.Environ(), "CONTROL_DATABASE_URL="+url)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "the binary started on a database it should refuse: %s", out)
	require.NoError(t, ctx.Err(), "the binary neither started nor exited: %s", out)
	return string(out)
}

// schemaVersion is what the migrations table records.
func schemaVersion(t *testing.T, db *pgxpool.Pool) (int, bool) {
	t.Helper()
	var version int
	var dirty bool
	require.NoError(t, db.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).
		Scan(&version, &dirty))
	return version, dirty
}

func exists(t *testing.T, db *pgxpool.Pool, table string) bool {
	t.Helper()
	var found bool
	require.NoError(t, db.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&found))
	return found
}

// tables are db's public tables other than the migrations table, sorted.
func tables(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT table_name::text FROM information_schema.tables
WHERE table_schema = 'public' AND table_name <> $1 ORDER BY 1`, migrationsTable)
	require.NoError(t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return out
}

// TestMigration_CreatesAnEmptyDatabaseFromTheFirst migrates an empty database to the newest
// schema, from migration 1.
func TestMigration_CreatesAnEmptyDatabaseFromTheFirst(t *testing.T) {
	url, db := newDatabase(t)

	_, p := startMigrating(t, url)

	version, dirty := schemaVersion(t, db)
	assert.Equal(t, latestVersion, version)
	assert.False(t, dirty)
	logged := logOf(t, p)
	assert.Contains(t, logged, fmt.Sprintf("store schema migrated from=0 to=%d", latestVersion))
	assert.NotContains(t, logged, "WARN")
	assert.Equal(t, schemaTables, tables(t, db))
}

// TestMigration_RefusesAnUnrecordedDatabaseThatHoldsTables refuses, untouched, a database with no
// migrations recorded that holds a table, as one a build before migrations made does (ADR 0069).
func TestMigration_RefusesAnUnrecordedDatabaseThatHoldsTables(t *testing.T) {
	url, db := newDatabase(t)
	_, err := db.Exec(context.Background(), `CREATE TABLE profiles (name text PRIMARY KEY)`)
	require.NoError(t, err)

	out := refusedOn(t, url)

	assert.Contains(t, out, "database schema refused")
	assert.Contains(t, out, "the database has no schema_migrations table and holds 1 other tables")
	assert.Contains(t, out, "Recreate it empty")
	assert.False(t, exists(t, db, migrationsTable), "the refused database was written to")
	assert.Equal(t, []string{"profiles"}, tables(t, db))
}

// TestMigration_RefusesADatabaseANewerBinaryMigrated refuses, untouched, a database at a version
// this binary does not know, and one a failed migration left dirty.
func TestMigration_RefusesADatabaseANewerBinaryMigrated(t *testing.T) {
	cases := []struct {
		name, update, message string
		version               int
		dirty                 bool
	}{
		{"newer", fmt.Sprintf(`UPDATE schema_migrations SET version = %d`, latestVersion+1),
			fmt.Sprintf("the database is at schema version %d and this binary knows up to %d", latestVersion+1, latestVersion),
			latestVersion + 1, false},
		{"dirty", `UPDATE schema_migrations SET dirty = true`,
			fmt.Sprintf("the database is recorded dirty at version %d", latestVersion), latestVersion, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, db := newDatabase(t)
			// Control: the binary migrates and starts on this database before it is changed.
			first, _ := startMigrating(t, url)
			require.NotEmpty(t, first)
			_, err := db.Exec(context.Background(), c.update)
			require.NoError(t, err)

			out := refusedOn(t, url)

			assert.Contains(t, out, "database schema refused")
			assert.Contains(t, out, c.message)
			version, dirty := schemaVersion(t, db)
			assert.Equal(t, c.version, version)
			assert.Equal(t, c.dirty, dirty)
		})
	}
}
