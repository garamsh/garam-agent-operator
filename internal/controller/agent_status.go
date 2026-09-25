package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// setSynced records on the Agent what this reconcile observed of the workload
// its spec asks for.
func setSynced(agent *agentv1alpha1.Agent, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionSynced,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agent.Generation,
	})
}

// setAvailable records on the Agent what this reconcile observed of the
// workload's readiness, which is a different question from whether the workload
// carries what the spec asks for.
func setAvailable(agent *agentv1alpha1.Agent, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionAvailable,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agent.Generation,
	})
}

// setAvailableFromWorkload reads the readiness of the StatefulSet this reconcile
// already holds. The message bounds what a ready replica is worth: the workload
// carries no readiness probe, so a container that started is ready whatever it
// is running.
func setAvailableFromWorkload(agent *agentv1alpha1.Agent, statefulSet *appsv1.StatefulSet) {
	// One replica is the whole workload, per ADR 0005, so a ready replica is
	// every replica.
	if statefulSet.Status.ReadyReplicas > 0 {
		setAvailable(agent, metav1.ConditionTrue, agentv1alpha1.ReasonReplicaReady,
			fmt.Sprintf("StatefulSet %q reports its replica ready. The workload carries no readiness probe, so this says the agent's containers are running and nothing about whether the agent inside them works",
				statefulSet.Name))

		return
	}

	setAvailable(agent, metav1.ConditionFalse, agentv1alpha1.ReasonReplicaNotReady,
		fmt.Sprintf("StatefulSet %q reports no ready replica. This covers a replica still starting as much as one that cannot start, and does not say which",
			statefulSet.Name))
}

// writeStatus writes the status this reconcile observed, and only when it says
// something the object does not already carry. held is the Agent as this
// reconcile read it, so the comparison is against the status the API server
// holds and not against a decision this reconcile made: Status is observed state
// and never an input to what Reconcile does.
//
// A patch and not an update: the poller reports status.agent on an Agent this
// operator constructed, and an update carries the resource version its object
// was read at, so a write landing between the read and it is refused. A merge
// patch carries no resource version and names only the fields this reconcile
// decided, which is what makes the two writers' disjoint fields disjoint writes.
func (r *AgentReconciler) writeStatus(ctx context.Context, agent, held *agentv1alpha1.Agent) error {
	if equality.Semantic.DeepEqual(held.Status, agent.Status) {
		return nil
	}

	if err := r.Status().Patch(ctx, agent, client.MergeFrom(held)); err != nil {
		return fmt.Errorf("patch agent status: %w", err)
	}

	reported := logf.FromContext(ctx)
	if synced := meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionSynced); synced != nil {
		reported = reported.WithValues("synced", synced.Status, "syncedReason", synced.Reason)
	}
	if available := meta.FindStatusCondition(agent.Status.Conditions, agentv1alpha1.ConditionAvailable); available != nil {
		reported = reported.WithValues("available", available.Status, "availableReason", available.Reason)
	}
	reported.Info("Reported on the Agent")

	return nil
}
