package controller

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
)

// AgentReconciler reconciles a Agent object
type AgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// CopyImage is the image the init container runs that copies an agent's
	// credential out of the volume the kubelet projects it into and into the one
	// the agent reads. It needs a shell and install, and nothing of the agent.
	CopyImage string

	// WorkspaceImage is the image the agent's workspace container runs: the
	// process serving the files an agent reads and writes and the commands it
	// executes. Empty builds the Pod with no workspace at all.
	WorkspaceImage string

	// AdapterImage is the image garam's adapter runs from, beside every agent
	// this operator constructed. Empty builds the Pod with no adapter at all.
	AdapterImage string

	// GaramAddress is the host and port of garam's machine listener, which the
	// adapter claims an agent's messages from.
	GaramAddress string

	// APIReader reads straight from the API server. The writer fence reads a
	// Pod's node and its claim through it, so that the controller holds no
	// informer over every node and claim in the cluster for a check it makes
	// only when a Pod is deleted.
	APIReader client.Reader

	// RenderAssignmentEpoch passes an agent its assignment epoch on the command
	// line. It is off until the agent image the deployment runs accepts the flag,
	// because one that does not refuses to start on it.
	RenderAssignmentEpoch bool

	// RenderInstructionsFile writes garam's reply instruction into an operator
	// instructions file the agent is passed as --instructions-file, wherever the
	// adapter is placed, and leaves the ego the spec's alone. It is off until the
	// agent image the deployment runs accepts the flag, which sherlock does from
	// v0.1.0, because one that does not refuses to start on it (ADR 0045).
	RenderInstructionsFile bool

	// MigrateSharedClaims replaces a StatefulSet whose workspace shares the
	// state claim with one claiming them separately (ADR 0044). Off, such a
	// StatefulSet is reconciled in the shape it has, and the Agent reports it as
	// not isolated, until a person has stopped the agent and copied its state
	// (ADR 0047).
	MigrateSharedClaims bool
}

// +kubebuilder:rbac:groups=agent.garam.sh,resources=agents,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=agent.garam.sh,resources=agents/status,verbs=patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get

// Reconcile drives the workload an Agent describes toward the Agent's spec, and
// reports on the Agent what it observed.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var agent agentv1alpha1.Agent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get agent: %w", err)
	}

	if agent.DeletionTimestamp != nil {
		return r.reconcileDeletion(ctx, &agent)
	}

	// Before anything is built, so that no Pod exists that the Agent could be
	// deleted out from under.
	if !controllerutil.ContainsFinalizer(&agent, workloadFencedFinalizer) {
		fenced := agent.DeepCopy()
		controllerutil.AddFinalizer(fenced, workloadFencedFinalizer)
		if err := r.Patch(ctx, fenced, client.MergeFrom(&agent)); err != nil {
			return ctrl.Result{}, fmt.Errorf("add the workload fence to the agent: %w", err)
		}
		agent = *fenced
	}

	held := agent.DeepCopy()

	// The fence first: a Pod being deleted is held or released on what it shows,
	// whatever the spec now asks of the workload.
	_, unverified, err := r.reconcileFence(ctx, &agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileWorkload(ctx, &agent); err != nil {
		return ctrl.Result{}, err
	}
	agent.Status.ObservedGeneration = agent.Generation

	if err := r.writeStatus(ctx, &agent, held); err != nil {
		return ctrl.Result{}, err
	}

	return fenceResult(unverified), nil
}

// reconcileDeletion takes a deleting Agent's workload down in order. The
// StatefulSet is deleted, which deletes its Pod; the Pod is held by its writer
// fence until its writers are seen to stop, with the evidence recorded on this
// Agent; and only once the Pod is gone does the Agent let itself go. A Pod
// whose fence stays unverified keeps the Agent deleting until a person
// releases the Pod.
func (r *AgentReconciler) reconcileDeletion(ctx context.Context, agent *agentv1alpha1.Agent) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(agent, workloadFencedFinalizer) {
		return ctrl.Result{}, nil
	}

	held := agent.DeepCopy()
	podGone, unverified, err := r.reconcileFence(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The Agent's own finalizer keeps the garbage collector from deleting what
	// it owns, so the StatefulSet is deleted here.
	statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: agent.Name, Namespace: agent.Namespace}}
	if err := r.Delete(ctx, statefulSet, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil &&
		!apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("delete the agent's statefulset: %w", err)
	}

	if err := r.writeStatus(ctx, agent, held); err != nil {
		return ctrl.Result{}, err
	}
	if !podGone {
		return fenceResult(unverified), nil
	}

	released := agent.DeepCopy()
	controllerutil.RemoveFinalizer(released, workloadFencedFinalizer)
	if err := r.Patch(ctx, released, client.MergeFrom(agent)); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("release the agent's workload fence: %w", err)
	}

	return ctrl.Result{}, nil
}

// fenceResult asks to read an unverified fence again, which re-reads it and
// never releases it.
func fenceResult(unverified bool) ctrl.Result {
	if unverified {
		return ctrl.Result{RequeueAfter: fenceRecheckInterval}
	}

	return ctrl.Result{}
}

// reconcileWorkload brings the workload the Agent describes to what its spec
// asks for, and records the outcome on the Agent's conditions. It returns an
// error only where retrying can fix the failure: a spec this controller cannot
// act on is a condition, because requeueing it would never end and the object
// would say nothing about why.
//
// The workload's readiness is read off the StatefulSet this reconcile already
// holds, so it costs no second read and is reported on a spec this controller
// cannot act on as readily as on one it can.
func (r *AgentReconciler) reconcileWorkload(ctx context.Context, agent *agentv1alpha1.Agent) error {
	descriptor, ok := resolveAgentType(agent.Spec.Type)
	if !ok {
		// The kubebuilder validation refused an unknown type at admission, so
		// this branch is reached only when the type is admitted but the
		// controller has not yet learned to build it. The workload is not built
		// until a controller version that knows the type is deployed; the Agent
		// is left to say so on Synced rather than failing the reconcile.
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonTypeUnimplemented,
			fmt.Sprintf("Agent type %q is admitted but not yet implemented by this controller version; the workload will not be built until one is",
				effectiveType(agent.Spec.Type)))
		setAvailable(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
			"The workload was not reconciled, so its readiness was not observed. The Synced condition says why")
		setStateIsolated(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
			"The workload was not reconciled, so its shape was not observed. The Synced condition says why")

		return nil
	}

	// Each Secret the workload reads is one whose absence leaves the Pod unable
	// to start, so each is waited for the same way.
	for _, required := range secretsRequiredBy(agent) {
		key := client.ObjectKey{Namespace: agent.Namespace, Name: required.name}
		if err := r.secretExists(ctx, key); err != nil {
			if apierrors.IsNotFound(err) {
				// Creating the Secret is not a spec edit and wakes nothing on its
				// own; the watch on Secrets is what brings this Agent back.
				setSynced(agent, metav1.ConditionFalse, required.missingReason,
					fmt.Sprintf("Secret %q does not exist, and the workload is not built until it does", key.Name))
				// Unknown and not False: this reconcile read no workload, and a
				// Secret deleted after one was built leaves that workload running.
				setAvailable(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
					"The workload was not reconciled, so its readiness was not observed. The Synced condition says why")
				setStateIsolated(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
					"The workload was not reconciled, so its shape was not observed. The Synced condition says why")

				return nil
			}

			return fmt.Errorf("get secret %q: %w", key.Name, err)
		}
	}

	// Before the StatefulSet, so the first Pod finds the Secret it copies from.
	if r.adapterBuilt(agent) {
		if err := r.ensurePlacementToken(ctx, agent); err != nil {
			return err
		}
	}

	statefulSet, err := r.reconcileStatefulSet(ctx, agent, descriptor)
	if errors.Is(err, errReplacing) {
		// The old StatefulSet's deletion is an event on a StatefulSet this Agent
		// owns, so it brings this Agent back to create the next one.
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonWorkloadReplacing,
			fmt.Sprintf("StatefulSet %q is being replaced to give the workspace a volume of its own; its Pod and its claims are kept", agent.Name))
		setAvailable(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
			"The workload was not reconciled, so its readiness was not observed. The Synced condition says why")
		setStateIsolated(agent, metav1.ConditionFalse, agentv1alpha1.ReasonWorkloadReplacing,
			fmt.Sprintf("StatefulSet %q is being replaced with one claiming the state and the workspace separately", agent.Name))

		return nil
	}
	if err != nil {
		return err
	}

	workspaceSize := workspaceStorageSize(agent)
	if claimed := claimedStorageSize(statefulSet, stateVolumeName); claimed.Cmp(agent.Spec.StorageSize) != 0 {
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonStorageSizeImmutable,
			fmt.Sprintf("The volume was claimed at %s and spec.storageSize now asks for %s, which a StatefulSet's claim template cannot be changed to",
				claimed.String(), agent.Spec.StorageSize.String()))
	} else if claimed := claimedStorageSize(statefulSet, workspaceVolumeName); hasWorkspaceClaim(statefulSet) &&
		claimed.Cmp(workspaceSize) != 0 {
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonStorageSizeImmutable,
			fmt.Sprintf("The workspace's volume was claimed at %s and the spec now asks for %s, which a StatefulSet's claim template cannot be changed to",
				claimed.String(), workspaceSize.String()))
	} else if claimedClass := claimedStorageClass(statefulSet); !ptr.Equal(claimedClass, agent.Spec.StorageClassName) {
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonStorageClassImmutable,
			fmt.Sprintf("The volume was claimed from storage class %s and spec.storageClassName now asks for %s, which a StatefulSet's claim template cannot be changed to",
				describeStorageClass(claimedClass), describeStorageClass(agent.Spec.StorageClassName)))
	} else {
		setSynced(agent, metav1.ConditionTrue, agentv1alpha1.ReasonWorkloadReconciled,
			fmt.Sprintf("StatefulSet %q carries what this Agent's spec asks for", statefulSet.Name))
	}

	setAvailableFromWorkload(agent, statefulSet)
	setStateIsolatedFromWorkload(agent, statefulSet)

	return nil
}

// effectiveType is the type the controller will build against, with the
// default substituted for the unset case. Used in messages so a reader sees
// what the controller saw rather than the literal empty string.
func effectiveType(specType string) string {
	if specType == "" {
		return agentTypeDefault
	}

	return specType
}

// requiredSecret is a Secret an Agent's workload reads, and the Synced reason
// its absence is reported under.
type requiredSecret struct {
	name          string
	missingReason string
}

// secretsRequiredBy lists the Secrets an Agent's workload cannot start without.
func secretsRequiredBy(agent *agentv1alpha1.Agent) []requiredSecret {
	required := []requiredSecret{{
		name: agent.Spec.CredentialsSecretName, missingReason: agentv1alpha1.ReasonCredentialsSecretMissing,
	}}
	if agent.Spec.Model != nil {
		required = append(required, requiredSecret{
			name: agent.Spec.Model.APIKeySecretRef.Name, missingReason: agentv1alpha1.ReasonModelKeySecretMissing,
		})
	}

	return required
}

// secretExists reads the metadata of a Secret an Agent names, and nothing else
// of it. The agent reads its credentials as mounted files and its model's key
// from its environment, so no part of this operator — its cache included —
// holds the key material.
func (r *AgentReconciler) secretExists(ctx context.Context, key client.ObjectKey) error {
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))

	return r.Get(ctx, key, secret)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.Agent{}).
		Owns(&appsv1.StatefulSet{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.agentsNamingSecret), builder.OnlyMetadata).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(agentOfPod)).
		Named("agent").
		Complete(r)
}

// agentOfPod maps an agent's Pod to its Agent, by the label the StatefulSet's
// template gives it, so that a Pod's deletion and its containers' states wake
// the Agent whose fence holds it.
func agentOfPod(_ context.Context, pod client.Object) []reconcile.Request {
	labels := pod.GetLabels()
	if labels["app.kubernetes.io/name"] != agentContainerName || labels["app.kubernetes.io/instance"] == "" {
		return nil
	}

	return []reconcile.Request{{NamespacedName: client.ObjectKey{
		Namespace: pod.GetNamespace(), Name: labels["app.kubernetes.io/instance"],
	}}}
}

// agentsNamingSecret maps a Secret to the Agents whose spec names it, so that
// the Secret's arrival wakes the Agents that were waiting for it.
func (r *AgentReconciler) agentsNamingSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var agents agentv1alpha1.AgentList
	if err := r.List(ctx, &agents, client.InNamespace(secret.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Agents for a Secret", "secret", secret.GetName())

		return nil
	}

	var requests []reconcile.Request
	for i := range agents.Items {
		for _, required := range secretsRequiredBy(&agents.Items[i]) {
			if required.name == secret.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})

				break
			}
		}
	}

	return requests
}
