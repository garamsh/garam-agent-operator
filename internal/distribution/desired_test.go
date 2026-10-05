package distribution_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
)

func TestDesired_ReleasesTheAgentsPlacedOnTheController(t *testing.T) {
	e := newEnv(t)

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	assert.Equal(t, map[string]string{agentA: "2", agentB: "2"}, got.agents)
	assert.NotEmpty(t, got.cursor)
}

func TestDesired_ForwardsTheLeafExactlyAsPresented(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.desired(t, e.withCert, "").status)

	// Every proof, the session's and each agent's, carries the presented leaf and nothing else.
	require.Len(t, e.prover.leaves, 3)
	for _, sent := range e.prover.leaves {
		block, rest := pem.Decode(sent)
		require.NotNil(t, block)
		assert.Equal(t, "CERTIFICATE", block.Type)
		assert.Equal(t, e.leaf, block.Bytes)
		assert.Empty(t, bytes.TrimSpace(rest))
	}
}

func TestDesired_RefusedWithoutAClientCertificate(t *testing.T) {
	e := newEnv(t)

	refused := e.desired(t, e.withoutCert, "")
	assert.Equal(t, 401, refused.status, refused.message)
	assert.Equal(t, 0, e.prover.sessions, "garam was asked about a request that presented no certificate")

	// Control: the same request presenting the controller's certificate is answered.
	assert.Equal(t, 200, e.desired(t, e.withCert, "").status)
}

func TestDesired_RefusesALeafForAnotherController(t *testing.T) {
	e := newEnv(t)
	e.prover.setSession(verdict{proof: distribution.Proof{Operator: elsewhere, Org: org}})

	refused := e.desired(t, e.withCert, "")
	assert.Equal(t, 403, refused.status, refused.message)

	// Control: a proof naming the certificate's own operator is accepted.
	e.prover.setSession(verdict{proof: distribution.Proof{Operator: controller, Org: org}})
	assert.Equal(t, 200, e.desired(t, e.withCert, "").status)
}

func TestDesired_RefusesAControllerGaramDoesNotProve(t *testing.T) {
	e := newEnv(t)
	e.prover.setSession(verdict{err: distribution.ErrNotProved})

	refused := e.desired(t, e.withCert, "")
	assert.Equal(t, 403, refused.status, refused.message)
	assert.Empty(t, refused.agents)

	// Control: the same request, proved, is answered.
	e.prover.setSession(verdict{proof: distribution.Proof{Operator: controller, Org: org}})
	assert.Equal(t, 200, e.desired(t, e.withCert, "").status)
}

func TestDesired_AnswersUndecidedWhileGaramIs(t *testing.T) {
	t.Run("session", func(t *testing.T) {
		e := newEnv(t)
		e.prover.setSession(verdict{err: distribution.ErrUndecided})
		assert.Equal(t, 503, e.desired(t, e.withCert, "").status)

		// Control: a decided session is answered.
		e.prover.setSession(verdict{proof: distribution.Proof{Operator: controller, Org: org}})
		assert.Equal(t, 200, e.desired(t, e.withCert, "").status)
	})
	t.Run("one agent", func(t *testing.T) {
		e := newEnv(t)
		e.prover.setAgentB(verdict{err: distribution.ErrUndecided})
		undecided := e.desired(t, e.withCert, "")
		assert.Equal(t, 503, undecided.status)
		assert.Empty(t, undecided.cursor, "an undecided answer moved the cursor")

		// Control: once garam decides, the same request releases both agents.
		e.prover.setAgentB(verdict{proof: distribution.Proof{Operator: controller, Org: org,
			Agent: &distribution.ProvenAgent{GRN: agentB, Epoch: epoch}}})
		assert.Equal(t, map[string]string{agentA: "2", agentB: "2"}, e.desired(t, e.withCert, "").agents)
	})
}

func TestDesired_WithholdsAnAgentAssignedElsewhere(t *testing.T) {
	e := newEnv(t)
	e.prover.setAgentB(verdict{err: distribution.ErrNotProved})

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	// Control: agentA, proved, is released beside the withheld agentB.
	assert.Equal(t, map[string]string{agentA: "2"}, got.agents)
}

func TestDesired_WithholdsAnAgentUnderAnotherEpoch(t *testing.T) {
	e := newEnv(t)
	e.prover.epochs[agentB] = "8"

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	// Control: agentA, under the epoch its revision was recorded for, is released.
	assert.Equal(t, map[string]string{agentA: "2"}, got.agents)
}

func TestDesired_NeverOffersAnAgentRecordedForAnotherController(t *testing.T) {
	e := newEnv(t)
	// garam would prove agentC here, but its latest revision was authorized for another controller.
	e.prover.epochs[agentC] = epoch

	got := e.desired(t, e.withCert, "")
	assert.NotContains(t, got.agents, agentC)
	// Control: once a revision of agentC is recorded for this controller, it is offered.
	e.configure(t, agentC, controller, 2)
	assert.Contains(t, e.desired(t, e.withCert, "").agents, agentC)
}

func TestDesired_AnswersTheWholeSetUnderEveryCursor(t *testing.T) {
	e := newEnv(t)
	first := e.desired(t, e.withCert, "")
	require.Equal(t, 200, first.status)

	// A cursor only says when to ask: the answer under it is the whole set again, not a delta.
	again := e.desired(t, e.withCert, "?after="+first.cursor)
	assert.Equal(t, first.agents, again.agents)
	assert.Equal(t, first.cursor, again.cursor)

	// Control: a revision stored since moves the cursor and is in the next whole set.
	e.configure(t, agentA, controller, 2)
	newer := e.desired(t, e.withCert, "?after="+first.cursor)
	assert.Equal(t, map[string]string{agentA: "3", agentB: "2"}, newer.agents)
	assert.NotEqual(t, first.cursor, newer.cursor)
}

func TestDesired_ReleasesAWithheldAgentOnceGaramProvesIt(t *testing.T) {
	e := newEnv(t)
	e.prover.setAgentB(verdict{err: distribution.ErrNotProved})
	withheld := e.desired(t, e.withCert, "")
	require.Equal(t, map[string]string{agentA: "2"}, withheld.agents)

	// Garam now proves agentB, as when a Deny is lifted. No revision is stored in between, so the
	// cursor has not moved, and the next answer still decides agentB again.
	e.prover.setAgentB(verdict{proof: distribution.Proof{Operator: controller, Org: org,
		Agent: &distribution.ProvenAgent{GRN: agentB, Epoch: epoch}}})
	released := e.desired(t, e.withCert, "?after="+withheld.cursor)
	assert.Equal(t, withheld.cursor, released.cursor)
	assert.Equal(t, map[string]string{agentA: "2", agentB: "2"}, released.agents)
}

func TestDesired_RefusesMoreAgentsThanOneAnswerCarries(t *testing.T) {
	e := newEnvCarrying(t, 2)
	// Control: as many candidates as one answer carries are answered.
	assert.Equal(t, 200, e.desired(t, e.withCert, "").status)

	e.configure(t, agentC, controller, 2)
	refused := e.desired(t, e.withCert, "")
	assert.Equal(t, 422, refused.status)
	assert.Equal(t, "too_many_agents", refused.kind)
	assert.Contains(t, refused.message, "more agents than one desired answer carries")
	assert.Empty(t, refused.agents)
}

func TestDesired_LongPollAnswersWhenARevisionIsStored(t *testing.T) {
	e := newEnv(t)
	first := e.desired(t, e.withCert, "")

	answered := make(chan feed, 1)
	go func() { answered <- e.desired(t, e.withCert, "?after="+first.cursor+"&waitSeconds=10") }()
	time.Sleep(100 * time.Millisecond)
	e.configure(t, agentB, controller, 2)

	select {
	case got := <-answered:
		assert.Equal(t, map[string]string{agentA: "2", agentB: "3"}, got.agents)
		assert.NotEqual(t, first.cursor, got.cursor)
	case <-time.After(5 * time.Second):
		t.Fatal("the long poll did not answer when a revision was stored")
	}
}

func TestDesired_LongPollIsBounded(t *testing.T) {
	e := newEnv(t)
	first := e.desired(t, e.withCert, "")

	start := time.Now()
	got := e.desired(t, e.withCert, "?after="+first.cursor+"&waitSeconds=1")
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
	assert.Equal(t, 200, got.status)
	assert.Equal(t, first.agents, got.agents)
	assert.Equal(t, first.cursor, got.cursor)
}

func TestDesired_RefusesAQueryItCannotAnswer(t *testing.T) {
	for _, query := range []string{"?waitSeconds=31", "?waitSeconds=-1", "?after=abc", "?after=-1", "?after=01", "?after=999"} {
		t.Run(query, func(t *testing.T) {
			e := newEnv(t)
			assert.Equal(t, 400, e.desired(t, e.withCert, query).status)
		})
	}
	// Control: the longest wait the feed allows, and a cursor it gave, are answered.
	e := newEnv(t)
	first := e.desired(t, e.withCert, "")
	e.configure(t, agentA, controller, 2)
	assert.Equal(t, 200, e.desired(t, e.withCert, "?after="+first.cursor+"&waitSeconds=30").status)
}

func TestDesired_MarksAnAgentCutOverFromItsLegacySource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const legacy = "grn:acme:default:agent:legacy"
	_, _, err := e.definitions.ImportCutover(ctx, definition.CutoverImport{
		Agent: legacy, Organization: "acme", ImportID: "import-1", Epoch: epoch, Assignee: controller, SourceDigest: "digest",
		Values: map[string]string{}, Dispositions: map[string]definition.Disposition{}, Profile: e.profile,
	})
	require.NoError(t, err)
	e.prover.epochs[legacy] = epoch

	// Imported and frozen, it is released to no controller.
	require.NoError(t, e.definitions.FreezeCutover(ctx, legacy, "import-1"))
	assert.NotContains(t, e.desired(t, e.withCert, "").agents, legacy)

	require.NoError(t, e.definitions.SwitchCutover(ctx, legacy, "import-1", "configure-ref"))
	switched := e.desired(t, e.withCert, "")
	assert.Equal(t, "1", switched.agents[legacy])
	assert.Equal(t, "cutover", switched.origins[legacy])

	// Control: an agent created here carries no origin.
	assert.Contains(t, switched.agents, agentA)
	assert.Empty(t, switched.origins[agentA])
}
