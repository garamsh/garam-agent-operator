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

// setMemoryMove records on the Agent where a move of its memory stands.
func setMemoryMove(agent *agentv1alpha1.Agent, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionMemoryMove,
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

// setWriterFence records on the Agent the controller's decision on its deleting
// Pod's fence.
func setWriterFence(agent *agentv1alpha1.Agent, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               agentv1alpha1.ConditionWriterFence,
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
	if heldStopped(agent) {
		setAvailable(agent, metav1.ConditionFalse, agentv1alpha1.ReasonSuspended,
			fmt.Sprintf("The spec %s the agent, so StatefulSet %q asks for no replica", stoppedBy(agent), statefulSet.Name))

		return
	}
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

// setRecovery records whether a recovery of the agent's credential is in
// progress, and, where the recovered certificate was refused, why.
func setRecovery(agent *agentv1alpha1.Agent, persisted bool, refusedAs string) {
	condition := metav1.Condition{
		Type:               agentv1alpha1.ConditionRecovery,
		Status:             metav1.ConditionFalse,
		Reason:             agentv1alpha1.ReasonNotRecovering,
		Message:            "No recovery of the agent's credential is in progress",
		ObservedGeneration: agent.Generation,
	}
	switch {
	case persisted && refusedAs == agentv1alpha1.ReasonRecoveredCertificateUnverified:
		condition.Status, condition.Reason = metav1.ConditionTrue, refusedAs
		condition.Message = "The recovered certificate does not verify against the issuer kept from the first " +
			"certificate, or is not over the persisted key, so it is not placed; the recovery request is kept"
	case persisted:
		condition.Status, condition.Reason = metav1.ConditionTrue, agentv1alpha1.ReasonRecovering
		condition.Message = "A recovery request is persisted and its recovered certificate is not placed yet"
	}
	meta.SetStatusCondition(&agent.Status.Conditions, condition)
}

// stoppedBy is what keeps the agent stopped, as a status message says it.
func stoppedBy(agent *agentv1alpha1.Agent) string {
	if agent.Spec.Suspended {
		return "suspends"
	}

	return "stops (spec.stopped, the control service's stop)"
}

// setSuspendedFromPod records whether the agent is stopped as its spec asks.
// True needs its Pod gone, not only asked to go: until then the Pod may still
// hold the state volume, and its writer fence says why it is held.
func setSuspendedFromPod(agent *agentv1alpha1.Agent, podGone bool) {
	condition := metav1.Condition{
		Type:               agentv1alpha1.ConditionSuspended,
		Status:             metav1.ConditionFalse,
		Reason:             agentv1alpha1.ReasonNotSuspended,
		Message:            "The spec does not suspend the agent",
		ObservedGeneration: agent.Generation,
	}
	switch {
	case heldStopped(agent) && podGone:
		condition.Status, condition.Reason = metav1.ConditionTrue, agentv1alpha1.ReasonSuspended
		condition.Message = fmt.Sprintf("The spec %s the agent and Pod %q is gone, so nothing mounts its volumes",
			stoppedBy(agent), agentPodName(agent))
	case heldStopped(agent):
		condition.Reason = agentv1alpha1.ReasonSuspending
		condition.Message = fmt.Sprintf("The spec %s the agent and Pod %q still exists. WriterFence says whether it is held",
			stoppedBy(agent), agentPodName(agent))
	}
	meta.SetStatusCondition(&agent.Status.Conditions, condition)
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
