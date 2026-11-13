package machineapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/agent/machineapi"
	"github.com/garamsh/garam-agent-operator/internal/gaon/token"
	"github.com/garamsh/garam-agent-operator/tests/gaon/garamfake"
	"github.com/garamsh/garam/lib/errorx"
	"github.com/garamsh/garam/lib/grn"
)

var (
	clientID = grn.New("org1", "operator", "op1").String()
	control  = grn.New("org1", "operator", "ctl1").String()
)

var agentID = grn.New("org1", "agent", "a1")

func agentGRN(t *testing.T) grn.GRN {
	t.Helper()
	require.NoError(t, agentID.Validate())
	return agentID
}

func newClient(t *testing.T) (*machineapi.Client, *garamfake.Server) {
	t.Helper()
	path, key := garamfake.WriteKey(t)
	fake := garamfake.New(t, clientID, &key.PublicKey)
	src, err := token.New(token.Options{
		MachineURL: fake.URL(), HTTPClient: fake.Client(), ClientID: clientID,
		TokenURL: fake.TokenURL(), KeyFile: path,
	})
	require.NoError(t, err)
	c, err := machineapi.New(fake.URL(), fake.Client(), src, control)
	require.NoError(t, err)
	return c, fake
}

func TestMintTokensMintsBothTokensAsTheOperator(t *testing.T) {
	c, fake := newClient(t)
	creds, err := c.MintTokens(context.Background(), agentGRN(t), 1)
	require.NoError(t, err)

	require.Contains(t, creds, machineapi.KeyAgentToken)
	require.Contains(t, creds, machineapi.KeyControlToken)
	assert.NotEqual(t, creds[machineapi.KeyAgentToken], creds[machineapi.KeyControlToken])

	mints := fake.Mints()
	require.Len(t, mints, 2)
	assert.Empty(t, mints[0].Audience)
	assert.Equal(t, control, mints[1].Audience)
	for _, m := range mints {
		assert.Equal(t, "machine-token.v1", m.Contract)
		assert.Equal(t, agentID.String(), m.Agent)
		assert.Equal(t, fake.Exchanges()[0].Token, m.Bearer)
	}
	assert.Len(t, fake.Exchanges(), 1)
}

func TestMintTokensRefusesATokenOfAnotherEpoch(t *testing.T) {
	c, fake := newClient(t)
	fake.SetEpoch(2)
	_, err := c.MintTokens(context.Background(), agentGRN(t), 1)
	require.ErrorIs(t, err, agent.ErrStaleEpoch)

	// Control: the same epoch is accepted.
	_, err = c.MintTokens(context.Background(), agentGRN(t), 2)
	require.NoError(t, err)
}

func TestReportStateSendsEpochAndState(t *testing.T) {
	c, fake := newClient(t)
	require.NoError(t, c.ReportState(context.Background(), agentGRN(t), 3, agent.StateProvisioned))
	reports := fake.Reports()
	require.Len(t, reports, 1)
	assert.Equal(t, 3, reports[0].Epoch)
	assert.Equal(t, "provisioned", reports[0].State)
	assert.Equal(t, agentID.String(), reports[0].Agent)
}

func TestReportStateMapsAStaleEpoch(t *testing.T) {
	cases := []struct {
		name    string
		refusal garamfake.Refusal
		stale   bool
		kind    errorx.Kind
	}{
		{"stale epoch", garamfake.Refusal{Status: 409, Kind: "failed_precondition", Message: "epoch"}, true, errorx.KindFailedPrecondition},
		{"other conflict", garamfake.Refusal{Status: 409, Kind: "aborted", Message: "busy"}, false, errorx.KindAborted},
		{"forbidden", garamfake.Refusal{Status: 403, Kind: "permission_denied", Message: "no"}, false, errorx.KindPermissionDenied},
		{"invalid", garamfake.Refusal{Status: 400, Kind: "invalid_argument", Message: "bad"}, false, errorx.KindInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fake := newClient(t)
			fake.RefuseReports(tc.refusal)
			err := c.ReportState(context.Background(), agentGRN(t), 1, agent.StateProvisioned)
			require.Error(t, err)
			assert.Equal(t, tc.stale, errors.Is(err, agent.ErrStaleEpoch))
			if !tc.stale {
				assert.Equal(t, tc.kind, errorx.KindOf(err))
			}
		})
	}
}

func TestATokenGaramRefusesIsExchangedAgainOnce(t *testing.T) {
	c, fake := newClient(t)
	_, err := c.MintTokens(context.Background(), agentGRN(t), 1)
	require.NoError(t, err)
	require.Len(t, fake.Exchanges(), 1)

	fake.Reject401(1)
	require.NoError(t, c.ReportState(context.Background(), agentGRN(t), 1, agent.StateProvisioned))
	assert.Len(t, fake.Exchanges(), 2)

	// A second refusal in a row is an error, not a loop.
	fake.Reject401(2)
	err = c.ReportState(context.Background(), agentGRN(t), 1, agent.StateProvisioned)
	require.Error(t, err)
	assert.Equal(t, errorx.KindUnauthenticated, errorx.KindOf(err))
	assert.Len(t, fake.Exchanges(), 3)
}
