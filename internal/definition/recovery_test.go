package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// recoveredPEM is the certificate every test's recovery is answered with.
const recoveredPEM = "recovered certificate"

// recoverBinding is what an agent:recover authority for a console request to firstAgent binds.
func recoverBinding(digest string) definition.Binding {
	b := binding(digest)
	b.Operation = "agent:recover"
	return b
}

// openRecovery is a console request opening recovery recoveryID of firstAgent under requestID.
func openRecovery(requestID, recoveryID string) definition.OpenRecoveryInput {
	return definition.OpenRecoveryInput{
		Key: key(requestID), Binding: recoverBinding(requestID), Agent: firstAgent, RequestID: recoveryID,
	}
}

// certificateRequest is k8s's certificate request for firstAgent's recovery recoveryID.
func certificateRequest(recoveryID, epoch, csr string) definition.InitialCertificateInput {
	return definition.InitialCertificateInput{
		Agent: firstAgent, Controller: k8s,
		Request: definition.CertificateRequest{RequestID: recoveryID, Epoch: epoch, CSRPEM: csr},
	}
}

func TestRecovery_StagesFollowInOrderAndTheBodyIsTheOnePrepared(t *testing.T) {
	ctx := context.Background()
	f := registered(t)

	opened, first, err := f.service.OpenRecovery(ctx, openRecovery("open-1", "rec-1"))
	require.NoError(t, err)
	require.True(t, first)
	assert.Equal(t, definition.RecoveryRequested, opened.Stage)
	assert.Equal(t, "1", opened.Epoch, "the recovery is not opened under the latest revision's epoch")

	_, err = f.service.FinalizeRecovery(ctx, firstAgent, "rec-1",
		definition.RecoveredCredential{Lineage: "l2", CertificatePEM: recoveredPEM})
	require.ErrorIs(t, err, definition.ErrRecoveryStage, "a recovery was finalized before it was prepared")

	prepared, err := f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	assert.Equal(t, definition.RecoveryPrepared, prepared.Stage)
	want, err := definition.RecoveryBody(definition.CertificateRequest{RequestID: "rec-1", Epoch: "1", CSRPEM: "csr"})
	require.NoError(t, err)
	assert.Equal(t, want, prepared.Body)
	assert.JSONEq(t, `{"requestId":"rec-1","epoch":"1","certificateRequestPem":"csr"}`, string(prepared.Body))

	// The identical request is answered as stored; another key under the same recovery is not.
	again, err := f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	assert.Equal(t, prepared, again)
	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "another csr"))
	require.ErrorIs(t, err, definition.ErrRequestReused)

	finalized, err := f.service.FinalizeRecovery(ctx, firstAgent, "rec-1",
		definition.RecoveredCredential{Lineage: "l2", CertificatePEM: recoveredPEM})
	require.NoError(t, err)
	assert.Equal(t, definition.RecoveryFinalized, finalized.Stage)
	assert.Equal(t, &definition.RecoveredCredential{Lineage: "l2", CertificatePEM: recoveredPEM}, finalized.Recovered)

	// The controller's repeat is answered the recovered credential.
	fetched, err := f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	assert.Equal(t, finalized.Recovered, fetched.Recovered)
}

func TestRecovery_OneIsOpenPerAgentAndAKeyIsOneRequest(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	_, _, err := f.service.OpenRecovery(ctx, openRecovery("open-1", "rec-1"))
	require.NoError(t, err)

	_, _, err = f.service.OpenRecovery(ctx, openRecovery("open-2", "rec-2"))
	require.ErrorIs(t, err, definition.ErrRecoveryOpen)

	changed := openRecovery("open-1", "rec-1")
	changed.Binding.BodySHA256 = other
	_, _, err = f.service.OpenRecovery(ctx, changed)
	require.ErrorIs(t, err, definition.ErrRequestReused)

	// Control: the repeat of the request is answered the recovery it opened.
	repeat, first, err := f.service.OpenRecovery(ctx, openRecovery("open-1", "rec-1"))
	require.NoError(t, err)
	assert.False(t, first)
	assert.Equal(t, "rec-1", repeat.RequestID)

	// Once it is finalized, another recovery opens, and the finalized one's identifier is not reused.
	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	_, err = f.service.FinalizeRecovery(ctx, firstAgent, "rec-1", definition.RecoveredCredential{Lineage: "l", CertificatePEM: "c"})
	require.NoError(t, err)
	_, _, err = f.service.OpenRecovery(ctx, openRecovery("open-3", "rec-1"))
	require.ErrorIs(t, err, definition.ErrRequestReused)
	_, first, err = f.service.OpenRecovery(ctx, openRecovery("open-2", "rec-2"))
	require.NoError(t, err)
	assert.True(t, first)
}

func TestRecovery_AnotherEpochOrOrganizationRefused(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	_, _, err := f.service.OpenRecovery(ctx, openRecovery("open-1", "rec-1"))
	require.NoError(t, err)

	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "2", "csr"))
	require.ErrorIs(t, err, definition.ErrRecoveryEpoch)
	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-unknown", "1", "csr"))
	require.ErrorIs(t, err, definition.ErrNotFound)

	elsewhere := openRecovery("open-2", "rec-2")
	elsewhere.Key.Organization = globex
	_, _, err = f.service.OpenRecovery(ctx, elsewhere)
	require.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.RecoveryOf(ctx, globex, firstAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: the recovery's own epoch prepares it, and its organization reads it.
	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	read, err := f.service.RecoveryOf(ctx, org, firstAgent)
	require.NoError(t, err)
	assert.Equal(t, definition.RecoveryPrepared, read.Stage)
}

func TestRecovery_TheFeedCarriesTheOpenRecoveryAndThePositionMoves(t *testing.T) {
	ctx := context.Background()
	f := registered(t)
	before, err := f.service.Position(ctx)
	require.NoError(t, err)
	page, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	require.Len(t, page.Revisions, 1)
	assert.Nil(t, page.Revisions[0].Recovery)

	_, _, err = f.service.OpenRecovery(ctx, openRecovery("open-1", "rec-1"))
	require.NoError(t, err)
	page, err = f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	assert.Greater(t, page.Position, before, "opening a recovery did not wake the feed")
	assert.Equal(t, &definition.OpenRecovery{RequestID: "rec-1", Epoch: "1"}, page.Revisions[0].Recovery)

	_, err = f.service.PrepareRecovery(ctx, certificateRequest("rec-1", "1", "csr"))
	require.NoError(t, err)
	_, err = f.service.FinalizeRecovery(ctx, firstAgent, "rec-1", definition.RecoveredCredential{Lineage: "l", CertificatePEM: "c"})
	require.NoError(t, err)
	finalized, err := f.service.Desired(ctx, k8s, 10)
	require.NoError(t, err)
	assert.Greater(t, finalized.Position, page.Position)
	assert.Nil(t, finalized.Revisions[0].Recovery)
}
