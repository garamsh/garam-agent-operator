//go:build e2e

package control_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The schemas #292's databases were made from, kept apart from the migrations that repeat them: the
// one the published 7c216469476d created, and the last schema.sql a dev build created before
// migrations.
const (
	publishedSchema = "../../test/testdata/schema-7c21646/schema.sql"
	lastFileSchema  = "../../test/testdata/schema-7b31d01/schema.sql"
)

// latestVersion is the newest migration the binary embeds, and organizationsVersion the migration
// that reads the earlier rows' organizations.
const (
	latestVersion        = 3
	organizationsVersion = 2
)

// migrationsTable is where the binary records the schema version.
const migrationsTable = "schema_migrations"

// laterTables are the tables migrations after the last schema.sql add, which it never made.
var laterTables = []string{"recoveries", "stops"}

// archiveTables are what migration 2 moves an earlier row with no source into.
var archiveTables = []string{"creations_n1", "profiles_n1", "templates_n1"}

// The earlier release's stored forms of a profile's settings and a template's configuration.
const (
	earlierSettings = `{"resources":{"limits":{"memory":"1Gi"}},"storageSize":"1Gi"}`
	earlierConfig   = `{"provider":"openai-compatible","baseURL":"https://api.minimax.io/v1","modelName":"MiniMax-M2",` +
		`"apiKeyRef":"model-keys/web","ego":"Answers web questions.","tools":{}}`
)

// The statements 7c21646 wrote its rows with (internal/definition/repository/postgres_queries.go at
// 7c21646), each as it ran there.
const (
	earlierPublishProfile = `
INSERT INTO profiles (name, version, settings)
SELECT $1::text, COALESCE(MAX(version), 0) + 1, $2::jsonb FROM profiles WHERE name = $1::text
RETURNING version`
	earlierPublishTemplate = `
INSERT INTO templates (name, version, profile_name, profile_version, config)
SELECT $1::text, COALESCE(MAX(version), 0) + 1, $2::text, $3::bigint, $4::jsonb FROM templates WHERE name = $1::text
RETURNING version`
	earlierInsertFirstDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, 1, $2::text, $3::bigint, $4::jsonb, (SELECT position FROM next), $5::text, $6::text`
	earlierAppendDefinition = `
WITH next AS (UPDATE positions SET position = position + 1 RETURNING position)
INSERT INTO definitions (agent, revision, profile_name, profile_version, config, position,
    assignment_operator, assignment_epoch)
SELECT $1::text, $2::bigint, $3::text, $4::bigint, $5::jsonb, (SELECT position FROM next), $6::text, $7::text
WHERE (SELECT MAX(revision) FROM definitions WHERE agent = $1::text) = $2::bigint - 1`
	earlierBeginRequest = `
INSERT INTO requests (organization, request_id, actor, operation, target, body_sha256,
    operation_ref, assignment_operator, assignment_epoch, agent, outcome)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'stale')
ON CONFLICT DO NOTHING`
	earlierApplyRequest = `
UPDATE requests SET outcome = 'applied', revision = $3
WHERE organization = $1 AND request_id = $2`
	earlierRecordStatus = `
INSERT INTO agent_status (agent, observed_revision, rendered_revision) VALUES ($1, $2, $3)
ON CONFLICT (agent) DO UPDATE SET
    observed_revision = GREATEST(agent_status.observed_revision, EXCLUDED.observed_revision),
    rendered_revision = GREATEST(agent_status.rendered_revision, EXCLUDED.rendered_revision)
RETURNING observed_revision, rendered_revision, applied_revision`
	earlierBeginCreation = `
INSERT INTO creations (organization, request_id, actor, template_name, template_version, state)
VALUES ($1, $2, $3, $4, $5, 'pending')
ON CONFLICT DO NOTHING`
	earlierRegisterCreation = `
UPDATE creations SET state = 'registered', agent = $3
WHERE organization = $1 AND request_id = $2`
)

// earlierRows are the rows seedEarlier wrote in one organization.
type earlierRows struct {
	org string
	// profile is named by template, and definitionProfile by agent's definitions only.
	profile, definitionProfile, template string
	// unnamedProfile and unnamedTemplate are named by nothing, so carry no organization.
	unnamedProfile, unnamedTemplate   string
	agent, createdAgent               string
	creationRequest, configureRequest string
}

// seedEarlier writes, in org, the rows 7c21646 stored: profiles and templates published, a creation
// registered, an agent's first revision and a configured second one with its request and status.
func seedEarlier(t *testing.T, db *pgxpool.Pool, org string) earlierRows {
	t.Helper()
	r := earlierRows{
		org: org, profile: name(t, "profile"), definitionProfile: name(t, "definition-profile"),
		template: name(t, "template"), unnamedProfile: name(t, "unnamed-profile"),
		unnamedTemplate: name(t, "unnamed-template"),
		agent:           "grn:" + org + ":default:agent:" + name(t, "agent"),
		createdAgent:    "grn:" + org + ":default:agent:" + name(t, "created"),
		creationRequest: name(t, "create"), configureRequest: name(t, "configure"),
	}
	run := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(context.Background(), query, args...)
		require.NoError(t, err, query)
	}
	for _, p := range []string{r.profile, r.definitionProfile, r.unnamedProfile} {
		run(earlierPublishProfile, p, earlierSettings)
	}
	for _, tpl := range []string{r.template, r.unnamedTemplate} {
		run(earlierPublishTemplate, tpl, r.profile, 1, earlierConfig)
	}
	const operator, epoch, actor = "control", "1", "grn:root:default:user:7c1d"
	run(earlierInsertFirstDefinition, r.agent, r.definitionProfile, 1, earlierConfig, operator, epoch)
	run(earlierBeginRequest, org, r.configureRequest, actor, "agent:configure", r.agent, "digest", "ref", operator,
		epoch, r.agent)
	run(earlierAppendDefinition, r.agent, 2, r.definitionProfile, 1, earlierConfig, operator, epoch)
	run(earlierApplyRequest, org, r.configureRequest, 2)
	run(earlierRecordStatus, r.agent, 2, 1)
	run(earlierBeginCreation, org, r.creationRequest, actor, r.template, 1)
	run(earlierRegisterCreation, org, r.creationRequest, r.createdAgent)
	return r
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

// applyFile runs the SQL file at path on db, as the binary before migrations ran its schema.
func applyFile(t *testing.T, db *pgxpool.Pool, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = db.Exec(context.Background(), string(body))
	require.NoError(t, err)
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

// catalogQuery describes db's public tables from the catalog, independently of the binary's own
// comparison: columns with type, nullability and default, constraints and indexes by definition.
const catalogQuery = `
SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' ||
    coalesce(column_default, '-')
FROM information_schema.columns WHERE table_schema = 'public'
UNION ALL
SELECT 'constraint ' || conrelid::regclass::text || ' ' || pg_get_constraintdef(oid)
FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND contype IN ('p', 'f', 'u', 'c')
UNION ALL
SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public'`

// indexNamed is the name an index's definition gives it, which differs as its constraint was
// created inline or added later.
var indexNamed = regexp.MustCompile(`INDEX \S+ ON`)

// catalog is db's schema as catalogQuery reads it, leaving out the tables in except.
func catalog(t *testing.T, db *pgxpool.Pool, except ...string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), catalogQuery)
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	var out []string
	for _, line := range lines {
		if slices.ContainsFunc(except, func(table string) bool {
			return strings.Contains(line, " "+table+".") || strings.Contains(line, " "+table+" ") ||
				strings.Contains(line, "public."+table+" ")
		}) {
			continue
		}
		out = append(out, indexNamed.ReplaceAllString(line, "INDEX ON"))
	}
	slices.Sort(out)
	return out
}

// lastFileCatalog is the schema the last schema.sql made on an empty database.
func lastFileCatalog(t *testing.T) []string {
	t.Helper()
	_, db := newDatabase(t)
	applyFile(t, db, lastFileSchema)
	return catalog(t, db)
}

// readRoute reads path from the binary at base under a real read authority on the organization.
func readRoute(t *testing.T, base, path, operation string) map[string]any {
	t.Helper()
	status, answer := sendTargetedTo(t, base, http.MethodGet, path, operation, real.orgGRN, name(t, "read"), nil, path)
	require.Equal(t, http.StatusOK, status, "%s: %v", path, answer)
	return answer
}

// TestMigration_UpgradesTheDatabaseThePublishedReleaseMade is #292: the current binary starts on a
// database the published 7c216469476d made and wrote to, keeps every row, reads each organization
// from what the rows carry, moves what carries none into an archive, and serves what it kept.
func TestMigration_UpgradesTheDatabaseThePublishedReleaseMade(t *testing.T) {
	ctx := context.Background()
	url, db := newDatabase(t)
	applyFile(t, db, publishedSchema)
	r := seedEarlier(t, db, real.orgID)

	base, p := startMigrating(t, url)

	version, dirty := schemaVersion(t, db)
	assert.Equal(t, latestVersion, version)
	assert.False(t, dirty)
	logged := logOf(t, p)
	assert.Contains(t, logged, "database made before migrations adopted version=1")

	// The organization each kept row carries: a profile from the template or definition naming it,
	// a template from the creation naming it, a definition from its agent's GRN. Each is unchanged.
	for _, profile := range []string{r.profile, r.definitionProfile} {
		var org string
		require.NoError(t, db.QueryRow(ctx, `SELECT organization FROM profiles
WHERE name = $1 AND version = 1 AND settings = $2::jsonb`, profile, earlierSettings).Scan(&org), profile)
		assert.Equal(t, r.org, org, profile)
	}
	var org string
	require.NoError(t, db.QueryRow(ctx, `SELECT organization FROM templates
WHERE name = $1 AND version = 1 AND profile_name = $2 AND profile_version = 1 AND config = $3::jsonb`,
		r.template, r.profile, earlierConfig).Scan(&org))
	assert.Equal(t, r.org, org)
	var definitions int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM definitions
WHERE agent = $1 AND organization = $2 AND profile_name = $3 AND config = $4::jsonb`,
		r.agent, r.org, r.definitionProfile, earlierConfig).Scan(&definitions))
	assert.Equal(t, 2, definitions)
	var outcome string
	var revision int
	require.NoError(t, db.QueryRow(ctx, `SELECT outcome, revision FROM requests WHERE request_id = $1`,
		r.configureRequest).Scan(&outcome, &revision))
	assert.Equal(t, "applied", outcome)
	assert.Equal(t, 2, revision)
	var observed, rendered int
	require.NoError(t, db.QueryRow(ctx, `SELECT observed_revision, rendered_revision FROM agent_status
WHERE agent = $1 AND applied_revision IS NULL AND applied_activation_id IS NULL`, r.agent).Scan(&observed, &rendered))
	assert.Equal(t, []int{2, 1}, []int{observed, rendered})

	// What carries no source for a column the schema now requires is in its archive, unchanged, and
	// logged by its key; nothing of it is left where the routes read.
	row := db.QueryRow(ctx, `SELECT actor, template_name, template_version, state, agent, reason, migrated_version
FROM creations_n1 WHERE organization = $1 AND request_id = $2 AND migrated_at IS NOT NULL`, r.org, r.creationRequest)
	var actor, templateName, state, agent string
	var templateVersion, migratedVersion int
	var reason *string
	require.NoError(t, row.Scan(&actor, &templateName, &templateVersion, &state, &agent, &reason, &migratedVersion))
	creation := []any{actor, templateName, templateVersion, state, agent, reason, migratedVersion}
	assert.Equal(t, []any{"grn:root:default:user:7c1d", r.template, 1, "registered", r.createdAgent, (*string)(nil), 2},
		creation)
	var creations int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM creations`).Scan(&creations))
	assert.Zero(t, creations)
	var archivedTemplate, archivedProfile int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM templates_n1 WHERE name = $1 AND version = 1
AND profile_name = $2 AND profile_version = 1 AND config = $3::jsonb AND migrated_version = 2`,
		r.unnamedTemplate, r.profile, earlierConfig).Scan(&archivedTemplate))
	assert.Equal(t, 1, archivedTemplate)
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM profiles_n1 WHERE name = $1 AND version = 1
AND settings = $2::jsonb AND migrated_version = 2`, r.unnamedProfile, earlierSettings).Scan(&archivedProfile))
	assert.Equal(t, 1, archivedProfile)
	var stray int
	require.NoError(t, db.QueryRow(ctx, `SELECT (SELECT count(*) FROM templates WHERE name = $1) +
(SELECT count(*) FROM profiles WHERE name = $2)`, r.unnamedTemplate, r.unnamedProfile).Scan(&stray))
	assert.Zero(t, stray)
	for _, archived := range []string{
		"archive=creations_n1 key=" + r.org + "/" + r.creationRequest,
		`archive=templates_n1 key="` + r.unnamedTemplate + ` version 1"`,
		`archive=profiles_n1 key="` + r.unnamedProfile + ` version 1"`,
	} {
		assert.Contains(t, logged, "WARN row archived by schema migration 2", archived)
		assert.Contains(t, logged, archived)
	}

	// The migrated schema is the one the last schema.sql made, beside the archives.
	assert.Equal(t, lastFileCatalog(t),
		catalog(t, db, append(append([]string{migrationsTable}, archiveTables...), laterTables...)...))

	// The routes serve what was kept, under the organization the migration read.
	orgPath := "/v1/orgs/" + r.org
	templates := readRoute(t, base, orgPath+"/templates", "agent-template:read")
	listed := firstVersion(r.template)
	listed["profile"] = firstVersion(r.profile)
	assert.Contains(t, templates["templates"], listed)
	assert.NotContains(t, fmt.Sprint(templates), r.unnamedTemplate)
	read := readRoute(t, base, orgPath+"/templates/"+r.template+"/versions/1", "agent-template:read")
	configuration := read["configuration"].(map[string]any)
	assert.Equal(t, "Answers web questions.", configuration["ego"])
	assert.Equal(t, "model-keys/web", configuration["model"].(map[string]any)["apiKeyRef"])
	profiles := readRoute(t, base, orgPath+"/profiles", "execution-profile:read")
	assert.Contains(t, profiles["profiles"], firstVersion(r.profile))
	assert.Contains(t, profiles["profiles"], firstVersion(r.definitionProfile))
	assert.NotContains(t, fmt.Sprint(profiles), r.unnamedProfile)
}

// TestMigration_CreatesAnEmptyDatabaseFromTheFirst is the control beside the upgrade: an empty
// database is migrated from version 1 to the schema the last schema.sql made, adopting nothing.
func TestMigration_CreatesAnEmptyDatabaseFromTheFirst(t *testing.T) {
	url, db := newDatabase(t)

	_, p := startMigrating(t, url)

	version, dirty := schemaVersion(t, db)
	assert.Equal(t, latestVersion, version)
	assert.False(t, dirty)
	logged := logOf(t, p)
	assert.NotContains(t, logged, "adopted")
	assert.Contains(t, logged, fmt.Sprintf("store schema migrated from=0 to=%d", latestVersion))
	assert.NotContains(t, logged, "WARN")
	for _, archive := range archiveTables {
		assert.False(t, exists(t, db, archive), archive)
	}
	assert.Equal(t, lastFileCatalog(t), catalog(t, db, append([]string{migrationsTable}, laterTables...)...))
}

// TestMigration_AdoptsADatabaseTheLastSchemaFileMade adopts, at version 2, a database a dev build
// made from the last schema.sql, keeping its rows and changing nothing else.
func TestMigration_AdoptsADatabaseTheLastSchemaFileMade(t *testing.T) {
	url, db := newDatabase(t)
	applyFile(t, db, lastFileSchema)
	profile := name(t, "profile")
	_, err := db.Exec(context.Background(), `INSERT INTO profiles (organization, name, version, settings)
VALUES ($1, $2, 1, $3::jsonb)`, real.orgID, profile, earlierSettings)
	require.NoError(t, err)
	before := catalog(t, db)

	base, p := startMigrating(t, url)

	version, dirty := schemaVersion(t, db)
	assert.Equal(t, latestVersion, version)
	assert.False(t, dirty)
	assert.Contains(t, logOf(t, p), "database made before migrations adopted version=2")
	assert.Equal(t, before, catalog(t, db, append([]string{migrationsTable}, laterTables...)...))
	profiles := readRoute(t, base, "/v1/orgs/"+real.orgID+"/profiles", "execution-profile:read")
	assert.Contains(t, profiles["profiles"], firstVersion(profile))
}

// TestMigration_RefusesADatabaseOfNoKnownSchema refuses, untouched, a database made before
// migrations whose schema is neither the published one nor the last schema.sql's, naming the
// difference.
func TestMigration_RefusesADatabaseOfNoKnownSchema(t *testing.T) {
	url, db := newDatabase(t)
	applyFile(t, db, lastFileSchema)
	_, err := db.Exec(context.Background(), `ALTER TABLE agent_status DROP COLUMN applied_generation`)
	require.NoError(t, err)
	before := catalog(t, db)

	out := refusedOn(t, url)

	assert.Contains(t, out, "database schema refused")
	assert.Contains(t, out, "missing column agent_status.applied_generation text null")
	assert.False(t, exists(t, db, migrationsTable), "the refused database was written to")
	assert.Equal(t, before, catalog(t, db))
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

// TestMigration_RefusesADefinitionWhoseAgentNamesNoOrganization stops migration 2 on a definition
// whose agent's GRN carries no organization, which is never archived, and keeps nothing of it: the
// earlier tables and rows stand, and the version is left dirty for an operator. The operator's step
// the refusal states, the row corrected by hand and the flag cleared with the statement it names,
// then lets the binary migrate.
func TestMigration_RefusesADefinitionWhoseAgentNamesNoOrganization(t *testing.T) {
	ctx := context.Background()
	url, db := newDatabase(t)
	applyFile(t, db, publishedSchema)
	profile, agent := name(t, "profile"), name(t, "agent")
	_, err := db.Exec(ctx, earlierPublishProfile, profile, earlierSettings)
	require.NoError(t, err)
	_, err = db.Exec(ctx, earlierInsertFirstDefinition, agent, profile, 1, earlierConfig, "control", "1")
	require.NoError(t, err)

	out := refusedOn(t, url)

	assert.Contains(t, out, "definitions hold an agent whose GRN names no organization: "+agent)
	assert.NotContains(t, out, "CREATE TABLE", "the refusal carries the migration's text, not its cause")
	version, dirty := schemaVersion(t, db)
	assert.Equal(t, organizationsVersion, version)
	assert.True(t, dirty)
	var kept int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM definitions WHERE agent = $1`, agent).Scan(&kept))
	assert.Equal(t, 1, kept)
	assert.False(t, exists(t, db, "definitions_v1"), "the failed migration kept part of its work")
	var organization bool
	require.NoError(t, db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
WHERE table_name = 'definitions' AND column_name = 'organization')`).Scan(&organization))
	assert.False(t, organization, "the failed migration kept part of its work")

	// The operator's step: correct the row, clear the flag with the statement the refusal names, and
	// start again.
	const clear = "UPDATE schema_migrations SET version = 1, dirty = false"
	// The log's text format escapes the command's double quotes; the statement is what must be exact.
	require.Contains(t, out, `CONTROL_DATABASE_URL\" -c '`+clear+`'`)
	corrected := "grn:" + real.orgID + ":default:agent:" + agent
	_, err = db.Exec(ctx, `UPDATE definitions SET agent = $1 WHERE agent = $2`, corrected, agent)
	require.NoError(t, err)
	_, err = db.Exec(ctx, clear)
	require.NoError(t, err)

	startMigrating(t, url)

	version, dirty = schemaVersion(t, db)
	assert.Equal(t, latestVersion, version)
	assert.False(t, dirty)
	var migrated string
	require.NoError(t, db.QueryRow(ctx, `SELECT organization FROM definitions WHERE agent = $1`, corrected).
		Scan(&migrated))
	assert.Equal(t, real.orgID, migrated)
}
