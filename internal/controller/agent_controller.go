package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	// ToolsImage is the image carrying the tool tree an agent loads its tools
	// from, mounted read-only into the agent's container. Empty builds the Pod
	// with no tool tree at all.
	ToolsImage string

	// WorkspaceImage is the image the agent's workspace container runs: the
	// process serving the files an agent reads and writes and the commands it
	// executes. Empty builds the Pod with no workspace at all.
	WorkspaceImage string
}

// +kubebuilder:rbac:groups=agent.garam.sh,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=agent.garam.sh,resources=agents/status,verbs=patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

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

	held := agent.DeepCopy()

	if err := r.reconcileWorkload(ctx, &agent); err != nil {
		return ctrl.Result{}, err
	}
	agent.Status.ObservedGeneration = agent.Generation

	if err := r.writeStatus(ctx, &agent, held); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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
	credentials := client.ObjectKey{Namespace: agent.Namespace, Name: agent.Spec.CredentialsSecretName}
	if err := r.credentialsExist(ctx, credentials); err != nil {
		if apierrors.IsNotFound(err) {
			// Creating the Secret is not a spec edit and wakes nothing on its
			// own; the watch on Secrets is what brings this Agent back.
			setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonCredentialsSecretMissing,
				fmt.Sprintf("Secret %q does not exist, and the workload is not built until it does", credentials.Name))
			// Unknown and not False: this reconcile read no workload, and a
			// Secret deleted after one was built leaves that workload running.
			setAvailable(agent, metav1.ConditionUnknown, agentv1alpha1.ReasonWorkloadNotObserved,
				"The workload was not reconciled, so its readiness was not observed. The Synced condition says why")

			return nil
		}

		return fmt.Errorf("get credentials secret: %w", err)
	}

	statefulSet, err := r.reconcileStatefulSet(ctx, agent)
	if err != nil {
		return err
	}

	if claimed := claimedStorageSize(statefulSet); claimed.Cmp(agent.Spec.StorageSize) != 0 {
		setSynced(agent, metav1.ConditionFalse, agentv1alpha1.ReasonStorageSizeImmutable,
			fmt.Sprintf("The volume was claimed at %s and spec.storageSize now asks for %s, which a StatefulSet's claim template cannot be changed to",
				claimed.String(), agent.Spec.StorageSize.String()))
	} else {
		setSynced(agent, metav1.ConditionTrue, agentv1alpha1.ReasonWorkloadReconciled,
			fmt.Sprintf("StatefulSet %q carries what this Agent's spec asks for", statefulSet.Name))
	}

	setAvailableFromWorkload(agent, statefulSet)

	return nil
}

// credentialsExist reads the metadata of the Secret an Agent names, and nothing
// else of it. The agent reads its credentials as mounted files, so no part of
// this operator — its cache included — holds the key material.
func (r *AgentReconciler) credentialsExist(ctx context.Context, key client.ObjectKey) error {
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
		Named("agent").
		Complete(r)
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
		if agents.Items[i].Spec.CredentialsSecretName == secret.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&agents.Items[i])})
		}
	}

	return requests
}
