package garam

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reporter tells garam what this operator sees of the pods of the agents it
// constructed. Until it runs, an agent this operator provisioned stays at
// garam's "pending" forever, and garam cannot tell an agent that never started
// from one that is running.
//
// It runs beside the Poller as a third manager.Runnable rather than inside the
// poll, on the reasoning ADR 0008 gave the Renewer: the two answer to different
// clocks, and a call one makes to garam should not hold the other's up. Its
// subject differs too — a poll reads garam's definitions and this reads the
// cluster's Agents, so folding it in would make one pass traverse both. Reading
// the Agents is also what makes it cheap: one list answers every agent, where a
// poll driven by definitions would read each agent's object separately.
type Reporter struct {
	client   *Client
	observer Observer
	interval time.Duration
}

// NewReporter returns a Reporter telling client what observer sees, every
// interval.
func NewReporter(client *Client, observer Observer, interval time.Duration) *Reporter {
	return &Reporter{client: client, observer: observer, interval: interval}
}

// Start reports until ctx is cancelled, which is what makes a Reporter a
// manager.Runnable. It returns no error: an error here stops the manager, and a
// garam that cannot be reached is what the next pass is for.
func (r *Reporter) Start(ctx context.Context) error {
	logf.FromContext(ctx).WithName("garam").Info(
		"Reporting what this operator sees of its agents' pods", "interval", r.interval)
	wait.UntilWithContext(ctx, r.report, r.interval)
	return nil
}

// report sends the state of every agent this operator holds a record of, once.
//
// Every pass reports every agent, and nothing here remembers what it last sent.
// garam keeps the last report and expires none of them, so a report skipped is
// a report that never happens and no later one corrects it; reporting
// unconditionally makes the stored state the last thing observed, where
// reporting on change would make it depend on this operator's memory of what it
// sent being right across restarts. What that costs is one request per agent per
// interval, against a route that answers 204 and records a value it already
// held.
//
// A refusal ends that agent's report and no more: the loop goes on to the next,
// because one agent this operator no longer holds says nothing about the rest.
func (r *Reporter) report(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("garam")

	observations, err := r.observer.Observations(ctx)
	if err != nil {
		log.Error(err, "Failed to read what this operator sees of its agents")
		return
	}
	log.V(1).Info("Read what this operator sees of its agents", "count", len(observations))

	for _, observation := range observations {
		state, reportable := provisioningState(observation.Readiness)
		if !reportable {
			continue
		}
		r.send(ctx, observation, state)
	}
}

// send reports one agent's state and says what garam answered where it refused.
//
// Neither refusal is retried and neither tears anything down. They are the
// second and third places this operator can learn it no longer holds an agent,
// and it acts on that nowhere: the credential in the pod is the agent's to renew
// and garam publishes nothing saying an operator should stop.
func (r *Reporter) send(ctx context.Context, observation Observation, state ProvisioningState) {
	log := logf.FromContext(ctx).WithName("garam")

	err := r.client.ReportProvisioningState(ctx, observation.Agent, observation.Epoch, state)
	switch {
	case errors.Is(err, ErrReportStale):
		// The agent was assigned again since this operator constructed it. No
		// route answers the epoch this operator holds it at, so nothing here
		// recovers one and every later report is refused the same way.
		log.Error(err, "Reported nothing: garam holds this agent at a later epoch",
			"agent", observation.Agent, "epoch", observation.Epoch)
		return
	case errors.Is(err, ErrAgentNotHeld):
		// garam refuses an agent assigned elsewhere and one that does not exist
		// identically, so there is nothing to tell apart and nothing to retry
		// against.
		log.Error(err, "Reported nothing: garam does not hold this agent for this operator",
			"agent", observation.Agent)
		return
	case err != nil:
		log.Error(err, "Failed to report what this operator sees of an agent's pod",
			"agent", observation.Agent)
		return
	}
	log.V(1).Info("Reported what this operator sees of an agent's pod",
		"agent", observation.Agent, "epoch", observation.Epoch, "state", state)
}

// provisioningState maps what this operator observed onto the state garam
// records, and reports whether there is anything to send.
//
// It is total over Readiness and reaches two of garam's four states; the type's
// own documentation says why the other two are out of reach. An unobserved
// workload sends nothing at all rather than a state standing in for one: garam
// already holds "pending" for an agent nobody has reported on, so staying silent
// leaves the true value standing where any report would replace it with a
// claim this operator cannot make.
func provisioningState(readiness Readiness) (ProvisioningState, bool) {
	switch readiness {
	case ReadinessReplicaReady:
		return StateReady, true
	case ReadinessNoReplica:
		return StateProvisioned, true
	case ReadinessUnobserved:
		return "", false
	default:
		return "", false
	}
}
