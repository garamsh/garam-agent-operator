//go:build e2e

package control_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// strandedAgent is an agent garam registered whose only creation an earlier release stored and the
// upgrade archived, on a database of its own, with the upgraded binary at base serving it.
type strandedAgent struct {
	grn, epoch, base, profile string
}

// strand has garam register a managed agent, stores its creation and first revision with 7c21646's
// own statements, which keep no agent:create reference, and upgrades that database under the
// current binary, as #309's test does.
func strand(t *testing.T) strandedAgent {
	t.Helper()
	ctx := context.Background()
	grn, epoch, err := real.createAgent(name(t, "stranded"))
	require.NoError(t, err)
	url, db := newDatabase(t)
	applyFile(t, db, publishedSchema)
	run := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, query, args...)
		require.NoError(t, err, query)
	}
	profile, template, request := name(t, "profile"), name(t, "template"), name(t, "create")
	run(earlierPublishProfile, profile, earlierSettings)
	run(earlierPublishTemplate, template, profile, 1, earlierConfig)
	run(earlierBeginCreation, real.orgID, request, "grn:root:default:user:7c1d", template, 1)
	run(earlierRegisterCreation, real.orgID, request, grn)
	run(earlierInsertFirstDefinition, grn, profile, 1, earlierConfig, real.controllerGRN, epoch)
	base, _ := startMigrating(t, url)
	return strandedAgent{grn: grn, epoch: epoch, base: base, profile: profile}
}

// TestRecovery_GivesAStrandedAgentItsFirstCertificate is #308: an agent stranded by ADR 0058, whose
// first certificate is refused creation_archived, is given one through credential recovery against
// a real garam, under the GRN it was registered with.
func TestRecovery_GivesAStrandedAgentItsFirstCertificate(t *testing.T) {
	a := strand(t)
	_, csr := certificateRequestPEM(t)
	status, refused := requestCertificateAt(t, a.base, a.grn, certificateRequestBody(name(t, "certificate"), a.epoch, csr))
	require.Equal(t, http.StatusConflict, status, refused)
	require.Equal(t, "creation_archived", refused["kind"])
	assert.Contains(t, refused["message"], "To keep the agent, its GRN, identity and memory, have an owner or admin "+
		"recover its credential", "the refusal does not name the route this test proves")

	store := &managerStore{agent: a.grn, unplaced: true}
	client := desired.NewClient(strings.TrimPrefix(a.base, "https://"),
		real.feedClient().Transport.(*http.Transport).TLSClientConfig)
	recoverer := desired.NewRecoverer(client, store)
	logged := funcr.New(func(prefix, args string) { t.Log(prefix, args) }, funcr.Options{})
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), logged))
	stopped := make(chan struct{})
	go func() {
		_ = recoverer.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	openID, recoveryID := name(t, "open"), name(t, "recovery")
	open := requestBody(t, struct {
		RequestID         string `json:"requestId"`
		RecoveryRequestID string `json:"recoveryRequestId"`
	}{openID, recoveryID})
	status, opened := sendMintedAt(t, a.base, a.grn, "/recovery", "agent:recover", openID, open)
	require.Equal(t, http.StatusCreated, status, opened)
	answer, err := client.Desired(ctx, "", 0)
	require.NoError(t, err)
	for _, agent := range answer.Agents {
		if agent.GRN == a.grn && agent.Recovery != nil {
			recoverer.Offer(map[string]desired.OpenRecovery{a.grn: *agent.Recovery})
		}
	}
	require.Eventually(t, func() bool { return readRecoveryAt(t, a.base, a.grn)["stage"] == "prepared" }, time.Minute,
		time.Second, "the recoverer did not prepare the recovery of an agent with no credential")

	prepared := []byte(readRecoveryAt(t, a.base, a.grn)["body"].(string))
	status, finalized := sendMintedAt(t, a.base, a.grn, "/recovery/finalize", "agent:recover", recoveryID, prepared)
	if status != http.StatusCreated {
		raw, _ := json.Marshal(finalized)
		require.Failf(t, "garam refused the recovery of an agent with no prior lineage",
			"status %d, answer %s", status, raw)
	}

	require.Eventually(t, func() bool { _, placed, _, _ := store.state(); return placed != nil }, time.Minute, time.Second,
		"the recoverer did not place the stranded agent's recovered credential")
	_, certificatePEM, lineage, refusedAs := store.state()
	assert.Empty(t, refusedAs)
	assert.NotEmpty(t, lineage)
	leaf := parsePEM(t, string(certificatePEM))
	require.Len(t, leaf.URIs, 1)
	assert.Equal(t, a.grn, leaf.URIs[0].String(), "the recovered certificate does not keep the agent's GRN")
	store.mu.Lock()
	written := store.written
	store.mu.Unlock()
	assert.Equal(t, finalized["issuerPem"], string(written.IssuerPEM), "the answered issuer is not what is written")
	assert.NotEmpty(t, written.ServerRootPEM)

	// The recovered pair runs the agent: its placement is registered, and a configure gives it a
	// revision with a reference garam activates under, under the GRN it was registered with.
	store.mu.Lock()
	keyPEM := store.key
	store.mu.Unlock()
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	require.NoError(t, err)
	placed := placedAgent{grn: a.grn, epoch: a.epoch, token: name(t, "placement-token"), pair: pair}
	status, raw, err := registerPlacementAt(a.base, a.grn, placed.placement(t))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, string(raw))
	// Revision 1 has no reference to be activated under: its creation's was never kept (ADR 0058).
	first := postAgentAt(t, a.base, placed, pair, "activations",
		activationOf(name(t, "activation"), a.epoch, strings.Repeat("1", 32)))
	assert.Equal(t, http.StatusForbidden, first.status, first.raw)
	assert.Equal(t, "not_authorized", first.body["kind"])

	configureID := name(t, "configure")
	configure := configureBody(configureID, a.profile, "after recovery", 1)
	status, configured := consoleSendAt(t, a.base, lifecycleRoute(a.grn, "/revisions"),
		mint(t, "agent:configure", a.grn, configureID, configure), configure)
	require.Equal(t, http.StatusOK, status, configured)
	second := postAgentAt(t, a.base, placed, pair, "activations",
		activationOfRevision(name(t, "activation"), a.epoch, strings.Repeat("2", 32), "2"))
	assert.Equal(t, http.StatusCreated, second.status, second.raw)
	assert.Equal(t, a.grn, second.body["grn"], "the agent was activated under another GRN")
}

// activationOfRevision is an activation of revision, as activationOf is of revision 1.
func activationOfRevision(requestID, epoch, generation, revision string) string {
	b, err := json.Marshal(struct {
		RequestID      string `json:"requestId"`
		Epoch          string `json:"epoch"`
		Generation     string `json:"generation"`
		ConfigRevision string `json:"configRevision"`
	}{requestID, epoch, generation, revision})
	if err != nil {
		panic(err)
	}
	return string(b)
}
