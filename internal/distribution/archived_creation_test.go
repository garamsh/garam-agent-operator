package distribution_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// switchedAgent stores grn's first revision on controller through a switched cutover, which
// stores no creation here, and has the prover place it at epoch.
func switchedAgent(t *testing.T, e *env, grn string) {
	t.Helper()
	ctx := context.Background()
	importID := "import-" + grn
	_, _, err := e.definitions.ImportCutover(ctx, definition.CutoverImport{
		Agent: definition.GRN(grn), Organization: orgID, ImportID: importID, Epoch: epoch, Assignee: controller,
		SourceDigest: "digest", Values: map[string]string{}, Dispositions: map[string]definition.Disposition{},
		Profile: e.profile,
	})
	require.NoError(t, err)
	require.NoError(t, e.definitions.FreezeCutover(ctx, definition.GRN(grn), importID))
	require.NoError(t, e.definitions.SwitchCutover(ctx, definition.GRN(grn), importID, "configure-ref"))
	e.prover.epochs[grn] = epoch
}

func TestRequestCertificate_AnAgentWhoseOnlyCreationIsArchivedIsRefused409(t *testing.T) {
	e := newEnv(t)
	const archived, unarchived = "grn:acme:default:agent:archived", "grn:acme:default:agent:unarchived"
	switchedAgent(t, e, archived)
	switchedAgent(t, e, unarchived)
	e.store.ArchiveRegistration(archived)
	csr := csrPEM(t, p256)

	status, raw, out := e.requestCertificate(t, e.withCert, archived, certificateBody("c1", epoch, csr))
	assert.Equal(t, http.StatusConflict, status, string(raw))
	assert.Equal(t, "creation_archived", out["kind"])
	assert.Contains(t, out["message"], "re-create the agent through the console's create route")
	assert.Equal(t, 0, e.issuer.calls(), "garam was asked with no reference")

	// An agent with no creation, archived or current, is still not found.
	status, raw, _ = e.requestCertificate(t, e.withCert, unarchived, certificateBody("c1", epoch, csr))
	assert.Equal(t, http.StatusNotFound, status, string(raw))

	// Control: an agent created here is issued its first certificate.
	status, raw, _ = e.requestCertificate(t, e.withCert, agentA, certificateBody("c1", epoch, csr))
	assert.Equal(t, http.StatusCreated, status, string(raw))
}
