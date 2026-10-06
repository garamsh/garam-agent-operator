package definition_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

const legacyAgent = definition.GRN("grn:acme:default:agent:1e9ac1")

func (f fixture) importLegacy(t *testing.T) {
	t.Helper()
	_, first, err := f.service.ImportCutover(context.Background(), definition.CutoverImport{
		Agent: legacyAgent, Organization: org, ImportID: "i1", Epoch: "3", Assignee: k8s, SourceDigest: "d",
		Profile: f.profile,
	})
	require.NoError(t, err)
	require.True(t, first)
}

func TestCutover_StagesFollowInOrderAndNoneReturnsASwitch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.importLegacy(t)

	require.ErrorIs(t, f.service.SwitchCutover(ctx, legacyAgent, "i1", "ref"), definition.ErrCutoverStage,
		"an import was switched before its freeze")
	require.ErrorIs(t, f.service.FreezeCutover(ctx, legacyAgent, "i2"), definition.ErrImportOpen)
	require.NoError(t, f.service.FreezeCutover(ctx, legacyAgent, "i1"))
	require.NoError(t, f.service.SwitchCutover(ctx, legacyAgent, "i1", "ref"))

	require.ErrorIs(t, f.service.FreezeCutover(ctx, legacyAgent, "i1"), definition.ErrCutoverStage)
	require.ErrorIs(t, f.service.RollBackCutover(ctx, legacyAgent, "i1"), definition.ErrReverseMigrationRequired)
	d, err := f.service.GetDefinition(ctx, legacyAgent)
	require.NoError(t, err)
	assert.Equal(t, &definition.Assignment{Operator: k8s, Epoch: "3"}, d.Assignment)
}

func TestCutover_RollbackDiscardsTheImportAndItsRevision(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.importLegacy(t)
	require.NoError(t, f.service.FreezeCutover(ctx, legacyAgent, "i1"))

	require.NoError(t, f.service.RollBackCutover(ctx, legacyAgent, "i1"))
	_, err := f.service.GetDefinition(ctx, legacyAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: a later import of the agent is a new one.
	f.importLegacy(t)
}

func TestCutover_AnAgentDefinedHereIsNotImported(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, registration{agent: firstAgent})
	_, _, err := f.service.CreateAgent(ctx, f.create("r1", f.template))
	require.NoError(t, err)

	_, _, err = f.service.ImportCutover(ctx, definition.CutoverImport{
		Agent: firstAgent, Organization: org, ImportID: "i1", Profile: f.profile,
	})
	require.ErrorIs(t, err, definition.ErrAlreadyDefined)

	// Control: an agent with no definition here is imported.
	f.importLegacy(t)
}

func TestCutover_AnotherOrganizationsProfileRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	imp := definition.CutoverImport{
		Agent: legacyAgent, Organization: globex, ImportID: "i1", Epoch: "3", Assignee: k8s, SourceDigest: "d",
		Profile: f.profile,
	}

	// f.profile is published in org only.
	_, _, err := f.service.ImportCutover(ctx, imp)
	require.ErrorIs(t, err, definition.ErrNotFound)
	_, err = f.service.GetDefinition(ctx, legacyAgent)
	require.ErrorIs(t, err, definition.ErrNotFound)

	// Control: once globex publishes a profile under that name and version, the same import is stored.
	_, err = f.service.PublishProfile(ctx, globex, f.profile.Name, settings("1", "2Gi"))
	require.NoError(t, err)
	_, first, err := f.service.ImportCutover(ctx, imp)
	require.NoError(t, err)
	assert.True(t, first)
	d, err := f.service.GetDefinition(ctx, legacyAgent)
	require.NoError(t, err)
	assert.Equal(t, globex, d.Organization)
}
