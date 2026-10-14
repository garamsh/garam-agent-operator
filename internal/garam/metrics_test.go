package garam_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

const (
	exchangeFailuresMetric = "garam_operator_token_exchange_failures_total"
	refusalsMetric         = "garam_operator_refusals_total"

	// metricInterval is short enough that a test sees several passes.
	metricInterval = 20 * time.Millisecond
)

// sampled answers the value the manager's registry exports for the series of
// name carrying labels, and zero where it exports none.
func sampled(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gather the manager's registry: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, series := range family.GetMetric() {
			matched := 0
			for _, pair := range series.GetLabel() {
				if labels[pair.GetName()] == pair.GetValue() {
					matched++
				}
			}
			if matched != len(labels) || len(series.GetLabel()) != len(labels) {
				continue
			}
			if series.GetCounter() != nil {
				return series.GetCounter().GetValue()
			}
			return series.GetGauge().GetValue()
		}
	}
	return 0
}

// refusals answers how many refusals the registry has counted against runnable
// under kind.
func refusals(t *testing.T, runnable, kind string) float64 {
	t.Helper()

	return sampled(t, refusalsMetric, map[string]string{"runnable": runnable, "kind": kind})
}

// run starts runnable and answers what stops it, which the test's end calls
// too where the test did not.
func run(t *testing.T, runnable manager.Runnable) (stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = runnable.Start(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-stopped
		})
	}
	t.Cleanup(stop)
	return stop
}

// TestReporterCountsARefusalByTheStatusGaramAnswered carries a refusal the
// client maps to one of its own errors, which is where the status would be lost
// if the mapping did not keep it.
func TestReporterCountsARefusalByTheStatusGaramAnswered(t *testing.T) {
	g := NewWithT(t)
	before := refusals(t, "reporter", "403")
	stub := newStubListener(t, func(w http.ResponseWriter, _ *http.Request) {
		answerJSON(w, http.StatusForbidden, `{"kind": "permission_denied", "message": "not assigned"}`)
	})
	observer := &recordingObserver{}
	observer.hold(garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady})

	run(t, garam.NewReporter(garam.NewClient(stub.address(), trustedBy(t, stub)), observer, metricInterval))

	g.Eventually(func() int { return len(stub.requests()) }, 5*time.Second).Should(BeNumerically(">=", 3))
	g.Expect(refusals(t, "reporter", "403") - before).To(BeNumerically(">=", 2))
}

// TestExchangeFailedCountsAFailedExchangeByAudienceAndReason is the series an
// alert on this operator's identity reads in place of the certificate's
// expiry: an exchange garam refuses as invalid_client is counted under the
// token's audience and that reason.
func TestExchangeFailedCountsAFailedExchangeByAudienceAndReason(t *testing.T) {
	g := NewWithT(t)
	labels := map[string]string{"audience": "control", "reason": "invalid_client"}
	before := sampled(t, exchangeFailuresMetric, labels)

	refusing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Garam-Contract-Version", "machine-token.v2")
		answerJSON(w, http.StatusBadRequest, `{"error":"invalid_client"}`)
	}))
	t.Cleanup(refusing.Close)
	source := machinetoken.NewSource(machinetoken.Config{
		Endpoint: refusing.URL + "/oauth/token", Files: writeKeyIssuance(t, t.TempDir()),
		HTTP: refusing.Client(), Failed: garam.ExchangeFailed(logr.Discard(), "control"),
	})
	_, err := source.Token(context.Background())
	g.Expect(err).To(MatchError(machinetoken.ErrInvalidClient))
	g.Expect(sampled(t, exchangeFailuresMetric, labels) - before).To(Equal(float64(1)))

	// The control: an exchange garam admits counts nothing.
	stub := newStubListener(t, answerNoDefinitions)
	before = sampled(t, exchangeFailuresMetric, labels)
	tlsConfig, err := garam.ServerTLS(stub.trustFile)
	g.Expect(err).NotTo(HaveOccurred())
	admitted := machinetoken.NewSource(machinetoken.Config{
		Endpoint: "https://" + stub.address() + tokenPath, Files: writeKeyIssuance(t, t.TempDir()),
		HTTP:   &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}},
		Failed: garam.ExchangeFailed(logr.Discard(), "control"),
	})
	_, err = admitted.Token(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(sampled(t, exchangeFailuresMetric, labels)).To(Equal(before))
}
