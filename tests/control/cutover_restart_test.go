//go:build e2e

package control_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restartAttached stops the attached binary and starts it again on the same database, as a
// restart of the control service would: nothing it held in memory survives. attachedURL moves to
// the new process.
func restartAttached(t *testing.T) {
	t.Helper()
	attached.stop()
	restarted, err := attachedStart()
	require.NoError(t, err)
	attached = restarted
}

// rows is agent's rows in table, each as JSON, so a test can put back what a stage removed.
func rows(t *testing.T, table, agent string) []string {
	t.Helper()
	got, err := pool.Query(t.Context(), "SELECT row_to_json(r)::text FROM "+table+" r WHERE agent = $1", agent)
	require.NoError(t, err)
	defer got.Close()
	var out []string
	for got.Next() {
		var row string
		require.NoError(t, got.Scan(&row))
		out = append(out, row)
	}
	require.NoError(t, got.Err())
	return out
}

// stage runs agent's stage under a fresh request and answers its status and body.
func stage(t *testing.T, agent, which string) (int, map[string]any) {
	t.Helper()
	requestID := name(t, which)
	body := requestBody(t, stageBody{requestID})
	if which == "switch" {
		body = switchOf(t, requestID)
	}
	return runStage(t, agent, which, body, requestID)
}

// TestCutover_ARestartBetweenStagesResumesAtTheStoredStage is #217's AC2 for import, freeze and
// switch: a restart of the control service between two stages, and one between garam answering a
// stage and the control service storing it, resume at the stage stored, neither repeating one nor
// skipping one.
func TestCutover_ARestartBetweenStagesResumesAtTheStoredStage(t *testing.T) {
	agent := legacyAgent(t)
	status, imported := importLegacy(t, agent, publishProfile(t, real.orgID))
	require.Equal(t, http.StatusCreated, status, imported)
	importRow := rows(t, "cutover_imports", agent)

	restartAttached(t)
	status, skipped := stage(t, agent, "switch")
	assert.Equal(t, http.StatusConflict, status, skipped)
	assert.Equal(t, "cutover_stage", skipped["kind"], "a restart let the switch skip the freeze")
	assert.Equal(t, importRow, rows(t, "cutover_imports", agent), "the refused switch changed the import")

	status, frozen := stage(t, agent, "freeze")
	require.Equal(t, http.StatusOK, status, frozen)
	assert.Equal(t, "frozen", frozen["stage"])

	// garam froze, and the control service stopped before it stored the freeze.
	_, err := pool.Exec(t.Context(), "UPDATE cutover_imports SET stage = 'imported' WHERE agent = $1", agent)
	require.NoError(t, err)
	restartAttached(t)
	status, refrozen := stage(t, agent, "freeze")
	require.Equal(t, http.StatusOK, status, refrozen)
	assert.Equal(t, "frozen", refrozen["stage"])

	restartAttached(t)
	status, switched := stage(t, agent, "switch")
	require.Equal(t, http.StatusOK, status, switched)
	assert.Equal(t, "switched", switched["stage"])
	switchedImport, switchedRevision := rows(t, "cutover_imports", agent), rows(t, "definitions", agent)

	// garam switched, and the control service stopped before it stored the switch.
	_, err = pool.Exec(t.Context(),
		"UPDATE cutover_imports SET stage = 'frozen', configure_ref = '' WHERE agent = $1", agent)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(),
		"UPDATE definitions SET assignment_operator = NULL, assignment_epoch = NULL WHERE agent = $1", agent)
	require.NoError(t, err)
	assert.NotContains(t, releasedCutovers(t), agent)
	restartAttached(t)
	status, reswitched := stage(t, agent, "switch")
	require.Equal(t, http.StatusOK, status, reswitched)
	assert.Equal(t, "switched", reswitched["stage"])
	assert.Equal(t, "1/cutover", releasedCutovers(t)[agent])
	assert.Equal(t, 1, count(t, "SELECT count(*) FROM definitions WHERE agent = $1", agent),
		"the resumed switch stored a second revision")
	assert.Len(t, rows(t, "cutover_imports", agent), len(switchedImport))
	assert.Len(t, rows(t, "definitions", agent), len(switchedRevision))

	// Control: a stage the stored one does not follow is still refused after a restart.
	restartAttached(t)
	status, refused := stage(t, agent, "freeze")
	assert.Equal(t, http.StatusConflict, status, refused)
	assert.Equal(t, "cutover_stage", refused["kind"])
}

// TestCutover_ARestartBeforeTheRollbackResumesAtTheStoredStage is #217's AC2 for the rollback: a
// restart between the freeze and the rollback ends with the import discarded. A restart between
// garam rolling back and the control service discarding the import does not resume today; that
// window is #286's.
func TestCutover_ARestartBeforeTheRollbackResumesAtTheStoredStage(t *testing.T) {
	agent := legacyAgent(t)
	status, imported := importLegacy(t, agent, publishProfile(t, real.orgID))
	require.Equal(t, http.StatusCreated, status, imported)
	status, frozen := stage(t, agent, "freeze")
	require.Equal(t, http.StatusOK, status, frozen)
	require.Len(t, rows(t, "cutover_imports", agent), 1)
	require.Len(t, rows(t, "definitions", agent), 1)

	restartAttached(t)
	status, rolledBack := stage(t, agent, "rollback")
	require.Equal(t, http.StatusOK, status, rolledBack)
	assert.Equal(t, "rolled_back", rolledBack["stage"])
	assert.Empty(t, rows(t, "cutover_imports", agent), "the import outlived the rollback")
	assert.Empty(t, rows(t, "definitions", agent), "the import's revision 1 outlived the rollback")

	// Control: the rollback is a stage, so the import it discarded cannot be frozen again.
	restartAttached(t)
	status, refused := stage(t, agent, "freeze")
	assert.Equal(t, http.StatusNotFound, status, refused)
}
