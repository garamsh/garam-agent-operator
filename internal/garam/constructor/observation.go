package constructor

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/garam"
)

// Observations returns what this operator holds about each agent it constructed
// into its namespace, read off the Agents themselves: the construction recorded
// the GRN and the epoch, and the reconciler records the readiness, so one list
// answers all three.
//
// An Agent carrying no GRN or no epoch is left out. That is an Agent a user
// wrote, which names no garam agent, and one this operator constructed before it
// recorded epochs, whose epoch garam answers on no route and which therefore has
// nothing it could be reported at.
func (a *Agent) Observations(ctx context.Context) ([]garam.Observation, error) {
	var agents agentv1alpha1.AgentList
	if err := a.client.List(ctx, &agents, client.InNamespace(a.namespace)); err != nil {
		return nil, fmt.Errorf("list the agents this operator constructed: %w", err)
	}

	observations := make([]garam.Observation, 0, len(agents.Items))
	for i := range agents.Items {
		constructed := &agents.Items[i]
		if constructed.Status.Agent == "" || constructed.Status.Epoch == 0 {
			continue
		}
		observations = append(observations, garam.Observation{
			Agent:     garam.GRN(constructed.Status.Agent),
			Epoch:     constructed.Status.Epoch,
			Readiness: readiness(constructed),
		})
	}
	return observations, nil
}

// readiness reads what the reconciler observed of an Agent's workload.
//
// Unknown and absent are the same answer here: the reconciler sets Unknown
// exactly where it read no workload, and a condition it has not written yet says
// the same thing. Neither is a state of the workload, so neither is reported.
func readiness(constructed *agentv1alpha1.Agent) garam.Readiness {
	available := meta.FindStatusCondition(constructed.Status.Conditions, agentv1alpha1.ConditionAvailable)
	if available == nil {
		return garam.ReadinessUnobserved
	}
	switch available.Status {
	case metav1.ConditionTrue:
		return garam.ReadinessReplicaReady
	case metav1.ConditionFalse:
		return garam.ReadinessNoReplica
	case metav1.ConditionUnknown:
		return garam.ReadinessUnobserved
	default:
		return garam.ReadinessUnobserved
	}
}
