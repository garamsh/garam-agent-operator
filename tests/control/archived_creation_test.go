//go:build e2e

package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestCertificateAt posts body on the controller route of the binary at base for agent,
// presenting the controller's leaf, and returns the status and the decoded answer.
func requestCertificateAt(t *testing.T, base, agent, body string) (int, map[string]string) {
	t.Helper()
	resp, err := real.feedClient().Post(base+"/v1/operators/self/agents/"+agent+"/certificate-requests",
		"application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := map[string]string{}
	require.NoError(t, json.Unmarshal(raw, &out), string(raw))
	return resp.StatusCode, out
}

// createAt creates an agent through the console's create route of the binary at base.
func createAt(t *testing.T, base, authority string, body []byte) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/orgs/"+real.orgID+"/agents", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Garam-Operation "+authority)
	resp, err := apiClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]string{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return resp.StatusCode, out
}

// TestMigration_AnAgentWhoseOnlyCreationTheUpgradeArchivedIsToldToBeRecreated reproduces #303: garam
// registers a managed agent, and its creation and first revision are stored with 7c21646's own
// statements, which keep no agent:create reference. After the upgrade, that agent's first
// certificate is refused with the step that recovers it; an agent whose archived creation was never
// registered is still not found, and an agent created after the upgrade is issued one.
func TestMigration_AnAgentWhoseOnlyCreationTheUpgradeArchivedIsToldToBeRecreated(t *testing.T) {
	ctx := context.Background()
	registered, epoch, err := real.createAgent(name(t, "registered"))
	require.NoError(t, err)
	unregistered, unregisteredEpoch, err := real.createAgent(name(t, "unregistered"))
	require.NoError(t, err)

	url, db := newDatabase(t)
	applyFile(t, db, publishedSchema)
	run := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, query, args...)
		require.NoError(t, err, query)
	}
	profile, template := name(t, "profile"), name(t, "template")
	run(earlierPublishProfile, profile, earlierSettings)
	run(earlierPublishTemplate, template, profile, 1, earlierConfig)
	const actor = "grn:root:default:user:7c1d"
	registeredRequest, unregisteredRequest := name(t, "create"), name(t, "create")
	run(earlierBeginCreation, real.orgID, registeredRequest, actor, template, 1)
	run(earlierRegisterCreation, real.orgID, registeredRequest, registered)
	run(earlierInsertFirstDefinition, registered, profile, 1, earlierConfig, real.controllerGRN, epoch)
	run(earlierBeginCreation, real.orgID, unregisteredRequest, actor, template, 1)
	run(earlierInsertFirstDefinition, unregistered, profile, 1, earlierConfig, real.controllerGRN, unregisteredEpoch)

	base, _ := startMigrating(t, url)
	var archived int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM creations_n1 WHERE request_id IN ($1, $2)`,
		registeredRequest, unregisteredRequest).Scan(&archived))
	require.Equal(t, 2, archived, "the upgrade did not archive both earlier creations")

	_, csr := certificateRequestPEM(t)
	status, out := requestCertificateAt(t, base, registered, certificateRequestBody(name(t, "certificate"), epoch, csr))
	assert.Equal(t, http.StatusConflict, status, out)
	assert.Equal(t, "creation_archived", out["kind"])
	assert.Contains(t, out["message"], "re-create the agent through the console's create route, which gives it a new GRN")
	assert.Contains(t, out["message"], "POST /v1/orgs/{org}/agents/{agent}/recovery under agent:recover")
	assert.Contains(t, out["message"], "To keep the agent")

	status, out = requestCertificateAt(t, base, unregistered,
		certificateRequestBody(name(t, "certificate"), unregisteredEpoch, csr))
	assert.Equal(t, http.StatusNotFound, status, out)

	// Control: an agent created through the upgraded binary's create route is issued its first
	// certificate under the reference that route stores.
	created := name(t, "template")
	run(`INSERT INTO templates (organization, name, version, profile_name, profile_version, config)
VALUES ($1, $2, 1, $3, 1, '{"ego":"created"}')`, real.orgID, created, profile)
	requestID := name(t, "create")
	body := createAgentBody(requestID, real.controllerGRN, created, profile)
	authority, err := mintCreate(requestID, body)
	require.NoError(t, err)
	status, made := createAt(t, base, authority, body)
	require.Equal(t, http.StatusCreated, status, made)
	status, out = requestCertificateAt(t, base, made["agent"],
		certificateRequestBody(name(t, "certificate"), made["epoch"], csr))
	assert.Equal(t, http.StatusCreated, status, out)
}

// TestCertificate_AnAgentWithNoCreationIsNotFoundWhereNoCreationWasArchived holds the archive's
// reader to a database migration 2 made no archive in, as the suite's own is.
func TestCertificate_AnAgentWithNoCreationIsNotFoundWhereNoCreationWasArchived(t *testing.T) {
	require.False(t, exists(t, pool, "creations_n1"), "the suite's database holds an archive")
	g := requireGaram(t)
	profile := seedRevision(t, g)
	// A configure records the latest revision for the controller garam assigned the agent to,
	// which the route requires before it looks for a creation.
	status, _ := configureKind(t, g, newConfigureRequest(name(t, "request"), profile, "configured", 1))
	require.Equal(t, http.StatusOK, status)
	_, csr := certificateRequestPEM(t)

	status, out := requestCertificateAt(t, attachedURL, g.agent(),
		certificateRequestBody(name(t, "certificate"), g.assignment, csr))
	assert.Equal(t, http.StatusNotFound, status, out)
}
