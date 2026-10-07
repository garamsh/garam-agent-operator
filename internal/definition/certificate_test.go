package definition_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// issuer is the test double for garam's initial-certificate issuance: it issues a certificate
// named for the request id unless the test set the error its calls answer, and records each call.
type issuer struct {
	mu        sync.Mutex
	err       error
	issuances []definition.Issuance
	// arrivals, when set, holds each call until as many calls as it counts have arrived.
	arrivals *sync.WaitGroup
}

func (i *issuer) Issue(_ context.Context, is definition.Issuance) (definition.IssuedCertificate, error) {
	if i.arrivals != nil {
		i.arrivals.Done()
		i.arrivals.Wait()
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.issuances = append(i.issuances, is)
	if i.err != nil {
		return definition.IssuedCertificate{}, i.err
	}
	return definition.IssuedCertificate{
		CertificatePEM: "certificate for " + is.Request.RequestID, IssuerPEM: "issuer", ServerRootPEM: "root",
		NotAfter: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}, nil
}

func (i *issuer) answer(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.err = err
}

// createdFixture is a fixture with firstAgent created on k8s under the request "r1".
func createdFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t, registration{agent: firstAgent})
	_, _, err := f.service.CreateAgent(context.Background(), f.create("r1", f.template))
	require.NoError(t, err)
	return f
}

func certificateInput(requestID, csr string) definition.InitialCertificateInput {
	return definition.InitialCertificateInput{
		Agent: firstAgent, Controller: k8s,
		Request: definition.CertificateRequest{RequestID: requestID, Epoch: "1", CSRPEM: csr},
	}
}

func TestRequestInitialCertificate_SendsTheCreationsReferenceAndStoresTheResult(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)

	c, first, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.NoError(t, err)
	assert.True(t, first)
	require.NotNil(t, c.Issued)
	assert.Equal(t, "certificate for c1", c.Issued.CertificatePEM)
	require.Len(t, f.issuer.issuances, 1)
	assert.Equal(t, "ref-r1", f.issuer.issuances[0].OperationRef, "the issuance was not sent under the creation's reference")

	// An identical request is answered from the store, and garam is not asked again.
	repeat, first, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.NoError(t, err)
	assert.False(t, first)
	assert.Equal(t, c, repeat)
	assert.Len(t, f.issuer.issuances, 1)
}

func TestRequestInitialCertificate_AnotherRequestAfterOneIsStoredRefused(t *testing.T) {
	for _, outcome := range []struct {
		name string
		err  error
	}{
		{"issued", nil},
		{"undecided", fmt.Errorf("no answer: %w", definition.ErrIssuanceUndecided)},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			ctx := context.Background()
			f := createdFixture(t)
			f.issuer.answer(outcome.err)
			_, _, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
			require.ErrorIs(t, err, outcome.err)

			for _, other := range []definition.InitialCertificateInput{
				certificateInput("c2", "csr"), certificateInput("c1", "another csr"),
			} {
				_, _, err = f.service.RequestInitialCertificate(ctx, other)
				require.ErrorIs(t, err, definition.ErrRequestReused)
			}

			// Control: the stored request itself is accepted, and issued once garam answers.
			f.issuer.answer(nil)
			c, _, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
			require.NoError(t, err)
			require.NotNil(t, c.Issued)
		})
	}
}

func TestRequestInitialCertificate_UndecidedRequestIsSentAgainUnchanged(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)
	f.issuer.answer(fmt.Errorf("no answer: %w", definition.ErrIssuanceUndecided))
	_, _, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.ErrorIs(t, err, definition.ErrIssuanceUndecided)

	f.issuer.answer(nil)
	c, first, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.NoError(t, err)
	assert.True(t, first)
	require.NotNil(t, c.Issued)
	require.Len(t, f.issuer.issuances, 2)
	assert.Equal(t, f.issuer.issuances[0], f.issuer.issuances[1], "the unknown outcome was retried as another request")
}

func TestRequestInitialCertificate_RefusalKeepsNothing(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)
	refusal := &definition.IssuanceRefusedError{Refusal: definition.RefusalForbidden, Kind: "permission_denied", Message: "no"}
	f.issuer.answer(refusal)

	_, _, err := f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.ErrorIs(t, err, refusal)

	// A corrected request is sent, not refused as another request than one stored.
	f.issuer.answer(nil)
	c, first, err := f.service.RequestInitialCertificate(ctx, certificateInput("c2", "corrected csr"))
	require.NoError(t, err)
	assert.True(t, first)
	assert.Equal(t, "certificate for c2", c.Issued.CertificatePEM)
}

func TestRequestInitialCertificate_AgentWithNoCreationRefused(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)
	in := certificateInput("c1", "csr")
	in.Agent = secondAgent

	_, _, err := f.service.RequestInitialCertificate(ctx, in)
	require.ErrorIs(t, err, definition.ErrNotFound)
	assert.Empty(t, f.issuer.issuances)

	// Control: the created agent's request is issued.
	_, _, err = f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.NoError(t, err)
}

func TestRequestInitialCertificate_ConcurrentIdenticalRequestsStoreOne(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)
	const n = 8
	var arrivals sync.WaitGroup
	arrivals.Add(n)
	f.issuer.arrivals = &arrivals

	// Every request has found the stored request pending before garam answers any of them.
	firsts := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, firsts[i], errs[i] = f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
		})
	}
	wg.Wait()
	recorded := 0
	for i := range n {
		require.NoError(t, errs[i])
		if firsts[i] {
			recorded++
		}
	}
	assert.Len(t, f.issuer.issuances, n, "the requests did not all reach garam before one was recorded")
	assert.Equal(t, 1, recorded, "more than one request recorded the certificate")
}

func TestRequestInitialCertificate_AnAgentWhoseOnlyCreationIsArchivedIsToldHowToKeepIt(t *testing.T) {
	ctx := context.Background()
	f := createdFixture(t)
	f.repository.ArchiveRegistration(secondAgent)
	in := certificateInput("c1", "csr")
	in.Agent = secondAgent

	_, _, err := f.service.RequestInitialCertificate(ctx, in)
	require.ErrorIs(t, err, definition.ErrCreationArchived)
	assert.ErrorContains(t, err, "re-create the agent through the console's create route, which gives it a new GRN")
	assert.ErrorContains(t, err, "To keep the agent, its GRN, identity and memory, have an owner or admin recover its credential")
	assert.ErrorContains(t, err, "and then configure it")
	assert.Empty(t, f.issuer.issuances, "garam was asked with no reference")

	// Control: an archived row does not shadow a current creation; the created agent is issued.
	f.repository.ArchiveRegistration(firstAgent)
	_, _, err = f.service.RequestInitialCertificate(ctx, certificateInput("c1", "csr"))
	require.NoError(t, err)
}
