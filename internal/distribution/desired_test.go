package distribution_test

import (
	"bytes"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/distribution"
)

func TestDesired_ReleasesTheAgentsPlacedOnTheController(t *testing.T) {
	e := newEnv(t)

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	assert.Equal(t, map[string]int64{agentA: 2, agentB: 2}, got.agents)
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
		e.prover.setAgent(agentB, verdict{err: distribution.ErrUndecided})
		undecided := e.desired(t, e.withCert, "")
		assert.Equal(t, 503, undecided.status)
		assert.Empty(t, undecided.cursor, "an undecided answer moved the cursor")

		// Control: once garam decides, the same request releases both agents.
		e.prover.setAgent(agentB, verdict{proof: distribution.Proof{Operator: controller, Org: org,
			Agent: &distribution.ProvenAgent{GRN: agentB, Epoch: epoch}}})
		assert.Equal(t, map[string]int64{agentA: 2, agentB: 2}, e.desired(t, e.withCert, "").agents)
	})
}

func TestDesired_WithholdsAnAgentAssignedElsewhere(t *testing.T) {
	e := newEnv(t)
	e.prover.setAgent(agentB, verdict{err: distribution.ErrNotProved})

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	// Control: agentA, proved, is released beside the withheld agentB.
	assert.Equal(t, map[string]int64{agentA: 2}, got.agents)
}

func TestDesired_WithholdsAnAgentUnderAnotherEpoch(t *testing.T) {
	e := newEnv(t)
	e.prover.epochs[agentB] = "8"

	got := e.desired(t, e.withCert, "")
	require.Equal(t, 200, got.status, got.message)
	// Control: agentA, under the epoch its revision was recorded for, is released.
	assert.Equal(t, map[string]int64{agentA: 2}, got.agents)
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

func TestDesired_CursorReleasesOnlyWhatIsNewer(t *testing.T) {
	e := newEnv(t)
	first := e.desired(t, e.withCert, "")
	require.Equal(t, 200, first.status)

	again := e.desired(t, e.withCert, "?after="+first.cursor)
	assert.Empty(t, again.agents)
	assert.Equal(t, first.cursor, again.cursor)

	// Control: a revision stored after the cursor is released, and only it.
	e.configure(t, agentA, controller, 2)
	newer := e.desired(t, e.withCert, "?after="+first.cursor)
	assert.Equal(t, map[string]int64{agentA: 3}, newer.agents)
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
		assert.Equal(t, map[string]int64{agentB: 3}, got.agents)
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
	assert.Empty(t, got.agents)
	assert.Equal(t, first.cursor, got.cursor)
}

func TestDesired_RefusesAQueryItCannotAnswer(t *testing.T) {
	for _, query := range []string{"?waitSeconds=31", "?waitSeconds=-1", "?after=abc", "?after=-1", "?after=999"} {
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
