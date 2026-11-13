package controlapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/agent"
	"github.com/garamsh/garam-agent-operator/internal/gaon/agent/controlapi"
	"github.com/garamsh/garam/lib/grn"
)

func TestDesiredReadsTheConfiguration(t *testing.T) {
	c := newControl(t)
	full := strings.Replace(agentJSON("a1", ""), `"configuration":{"model":{},"ego":"","tools":{}}`,
		`"configuration":{"model":{"provider":"openai","baseUrl":"https://llm.example/v1","name":"m",`+
			`"apiKeyRef":"creds/key","embedding":{"baseUrl":"https://emb.example/v1","name":"e"}},`+
			`"ego":"hello\n","tools":{"web":"sha256:abc"}}`, 1)
	c.reply(200, feedJSON("1", full, agentJSON("a2", "")))

	got, err := c.client.Desired(t.Context())

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, agent.Configuration{
		Model: &agent.Model{
			Provider: "openai", BaseURL: "https://llm.example/v1", Name: "m", APIKeyRef: "creds/key",
			Embedding: &agent.Embedding{BaseURL: "https://emb.example/v1", Name: "e"},
		},
		Ego: "hello\n", Tools: map[string]string{"web": "sha256:abc"},
	}, got[0].Configuration)
	assert.Nil(t, got[1].Configuration.Model, "an empty model is no model")
	assert.Empty(t, got[1].Configuration.Tools)
}

var placement = agent.Placement{Epoch: "3", PodUID: "pod-uid", PVCUID: "pvc-uid", TokenSHA256: strings.Repeat("a", 64)}

func TestRegisterPlacementPostsTheContractsBody(t *testing.T) {
	g := grn.New("org1", "agent", "a1")
	for status, created := range map[int]bool{201: true, 200: false} {
		c := newControl(t)
		c.reply(status, `{}`)

		got, err := c.client.RegisterPlacement(t.Context(), g, placement)

		require.NoError(t, err, status)
		assert.Equal(t, created, got, status)
		reqs := c.requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, http.MethodPost, reqs[0].Method)
		assert.Contains(t, reqs[0].Path, "/v1/operators/self/agents/")
		assert.True(t, strings.HasSuffix(reqs[0].Path, "/placements"), reqs[0].Path)
		assert.Equal(t, manager, reqs[0].Header.Get(controlapi.ControllerHeader))
		assert.Equal(t, "Bearer garam_ot_0", reqs[0].Header.Get("Authorization"))
		assert.JSONEq(t, `{"epoch":"3","podUid":"pod-uid","pvcUid":"pvc-uid",`+
			`"tokenSha256":"`+placement.TokenSHA256+`","previous":null}`, reqs[0].Body)
	}
}

func TestRegisterPlacementSortsTheRefusals(t *testing.T) {
	g := grn.New("org1", "agent", "a1")
	for _, tc := range []struct {
		name   string
		status int
		kind   string
		want   error
	}{
		{"placement_superseded", 409, "placement_superseded", agent.ErrPlacementSuperseded},
		{"epoch_superseded", 409, "epoch_superseded", agent.ErrPlacementSuperseded},
		{"previous_mismatch", 409, "previous_mismatch", agent.ErrPlacementRefused},
		{"placement_conflict", 409, "placement_conflict", agent.ErrPlacementRefused},
		{"evidence_missing", 409, "evidence_missing", agent.ErrPlacementRefused},
		{"forbidden", 403, "forbidden", agent.ErrPlacementRefused},
		{"a superseded kind under another status", 403, "placement_superseded", agent.ErrPlacementRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newControl(t)
			c.reply(tc.status, fmt.Sprintf(`{"kind":%q,"message":"m"}`, tc.kind))

			created, err := c.client.RegisterPlacement(t.Context(), g, placement)

			assert.False(t, created)
			require.ErrorIs(t, err, tc.want)
			other := agent.ErrPlacementRefused
			if tc.want == agent.ErrPlacementRefused {
				other = agent.ErrPlacementSuperseded
			}
			assert.NotErrorIs(t, err, other)
		})
	}
}

func TestRegisterPlacementLeavesATransientFailureForTheNextPass(t *testing.T) {
	g := grn.New("org1", "agent", "a1")
	for _, status := range []int{500, 503} {
		c := newControl(t)
		c.reply(status, `{"kind":"internal","message":"m"}`)

		_, err := c.client.RegisterPlacement(t.Context(), g, placement)

		require.Error(t, err, status)
		assert.NotErrorIs(t, err, agent.ErrPlacementSuperseded, status)
		assert.NotErrorIs(t, err, agent.ErrPlacementRefused, status)
	}
}
