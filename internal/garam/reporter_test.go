package garam_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/garamsh/garam-agent-operator/internal/garam"
)

const (
	// reportInterval is short enough that a test sees several passes.
	reportInterval = 20 * time.Millisecond

	// reportTimeout bounds what a test waits for the reporter to have done.
	reportTimeout = 5 * time.Second

	readyAgent       = garam.GRN("grn:acme:default:agent:9f2ac1b40d8e7a35")
	unreadyAgent     = garam.GRN("grn:acme:default:agent:0a1b2c3d4e5f6071")
	unobservedAgent  = garam.GRN("grn:acme:default:agent:1122334455667788")
	reportedAtEpoch  = int64(7)
	provisioningPath = "/provisioning-state"
)

// recordingObserver stands in for the cluster this operator reads its own
// agents back from, which is the module boundary a Reporter is written against.
type recordingObserver struct {
	mu sync.Mutex

	observations []garam.Observation

	// refuse is what Observations answers instead of a reading, where it is set.
	refuse error
}

func (o *recordingObserver) Observations(_ context.Context) ([]garam.Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.refuse != nil {
		return nil, o.refuse
	}
	return append([]garam.Observation(nil), o.observations...), nil
}

func (o *recordingObserver) hold(observations ...garam.Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observations = observations
}

// runReporter reports to stub until the test ends, and fails the test where
// Start answers an error rather than stopping quietly.
func runReporter(t *testing.T, stub *stubListener, observer garam.Observer) {
	t.Helper()

	reporter := garam.NewReporter(garam.NewClient(stub.address(), trustedBy(t, stub)), observer, reportInterval)
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), testr.New(t)))
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := reporter.Start(ctx); err != nil {
			t.Errorf("the reporter stopped with %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
}

// reported is the agent and body of every provisioning report the stub served,
// in the order it served them.
func reported(stub *stubListener) []stubRequest {
	var reports []stubRequest
	for _, request := range stub.requests() {
		if strings.HasSuffix(request.path, provisioningPath) {
			request.path = strings.TrimSuffix(strings.TrimPrefix(request.path, "/agents/"), provisioningPath)
			reports = append(reports, request)
		}
	}
	return reports
}

// reportedAgents is the agent of every provisioning report the stub served, in
// the order it served them.
func reportedAgents(stub *stubListener) []string {
	reports := reported(stub)
	agents := make([]string, 0, len(reports))
	for _, report := range reports {
		agents = append(agents, report.path)
	}
	return agents
}

// recordEveryReport answers every report the way garam's listener does.
func recordEveryReport(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func TestReporterTellsGaramReadyForAReadyReplicaAndProvisionedForNone(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)
	observer := &recordingObserver{}
	observer.hold(
		garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady},
		garam.Observation{Agent: unreadyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessNoReplica},
	)

	runReporter(t, stub, observer)

	g.Eventually(func() map[string]string {
		states := map[string]string{}
		for _, report := range reported(stub) {
			states[report.path] = report.body
		}
		return states
	}, reportTimeout).Should(Equal(map[string]string{
		string(readyAgent):   `{"epoch":7,"state":"ready"}`,
		string(unreadyAgent): `{"epoch":7,"state":"provisioned"}`,
	}))
}

// TestReporterTellsGaramNothingAboutAWorkloadItDidNotObserve is what keeps this
// operator from asserting more than it saw. garam's "pending" means no operator
// has reported yet and garam already holds it, so the way to leave that true
// value standing is to send nothing rather than to send a state standing in for
// one.
func TestReporterTellsGaramNothingAboutAWorkloadItDidNotObserve(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)
	observer := &recordingObserver{}
	observer.hold(
		garam.Observation{Agent: unobservedAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessUnobserved},
		// The observed agent beside it, so that a pass reaching neither cannot
		// be read as the unobserved one being withheld.
		garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady},
	)

	runReporter(t, stub, observer)

	g.Eventually(func() []string { return reportedAgents(stub) }, reportTimeout).
		Should(ContainElement(string(readyAgent)))
	g.Consistently(func() []string { return reportedAgents(stub) }, 200*time.Millisecond).
		ShouldNot(ContainElement(string(unobservedAgent)))
}

// TestReporterReportsOnEveryPassRatherThanOnChange is what the latch on garam's
// side asks for. garam keeps the last report and expires none, so a report
// skipped is one that never happens and no later pass corrects it; reporting
// unconditionally is what keeps the stored state equal to the last thing
// observed without this operator having to remember what it sent.
func TestReporterReportsOnEveryPassRatherThanOnChange(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)
	observer := &recordingObserver{}
	observer.hold(garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady})

	runReporter(t, stub, observer)

	// Nothing about the agent changes between passes, so a reporter that
	// reported on change would send one and stop.
	g.Eventually(func() int { return len(reported(stub)) }, reportTimeout).Should(BeNumerically(">=", 3))
}

// TestReporterGoesOnToTheNextAgentAfterARefusal is criterion 4 of issue #117:
// one agent this operator no longer holds says nothing about the rest, so a
// refusal ends that agent's report and not the pass.
func TestReporterGoesOnToTheNextAgentAfterARefusal(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, string(unreadyAgent)) {
			answerJSON(w, http.StatusConflict,
				`{"kind": "failed_precondition", "message": "the assignment is at a later epoch"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	observer := &recordingObserver{}
	observer.hold(
		// The refused agent first, so that a pass stopping at a refusal would
		// never reach the one behind it.
		garam.Observation{Agent: unreadyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessNoReplica},
		garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady},
	)

	runReporter(t, stub, observer)

	g.Eventually(func() []string { return reportedAgents(stub) }, reportTimeout).
		Should(ContainElement(string(readyAgent)))
}

// TestReporterKeepsReportingAfterAReadThatFailed keeps a reading this operator
// could not take from stopping the Runnable, which would leave garam holding
// whatever it last heard with nothing saying why it stopped hearing.
func TestReporterKeepsReportingAfterAReadThatFailed(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)
	observer := &recordingObserver{refuse: errors.New("the API server refused a list")}

	runReporter(t, stub, observer)

	g.Consistently(func() int { return len(reported(stub)) }, 100*time.Millisecond).Should(BeZero())

	observer.mu.Lock()
	observer.refuse = nil
	observer.mu.Unlock()
	observer.hold(garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady})

	g.Eventually(func() int { return len(reported(stub)) }, reportTimeout).Should(BeNumerically(">", 0))
}

// TestReporterCallsGaramNotAtAllWhereItObservesNothing is the property that
// keeps an operator with no agents quiet. It is the same shape as an operator
// given no garam address, which constructs no Reporter at all.
func TestReporterCallsGaramNotAtAllWhereItObservesNothing(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)

	runReporter(t, stub, &recordingObserver{})

	g.Consistently(func() int { return len(stub.requests()) }, 200*time.Millisecond).Should(BeZero())
}

func TestReporterStopsWhenItsContextIsCancelled(t *testing.T) {
	g := NewWithT(t)
	stub := newStubListener(t, recordEveryReport)
	observer := &recordingObserver{}
	observer.hold(garam.Observation{Agent: readyAgent, Epoch: reportedAtEpoch, Readiness: garam.ReadinessReplicaReady})

	reporter := garam.NewReporter(garam.NewClient(stub.address(), trustedBy(t, stub)), observer, reportInterval)
	ctx, cancel := context.WithCancel(logf.IntoContext(context.Background(), testr.New(t)))
	stopped := make(chan error, 1)
	go func() { stopped <- reporter.Start(ctx) }()

	g.Eventually(func() int { return len(reported(stub)) }, reportTimeout).Should(BeNumerically(">", 0))
	cancel()
	g.Eventually(stopped, reportTimeout).Should(Receive(BeNil()))
}
