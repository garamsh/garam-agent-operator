package constructor_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	agentv1alpha1 "github.com/garamsh/garam-agent-operator/api/v1alpha1"
	"github.com/garamsh/garam-agent-operator/internal/garam"
	"github.com/garamsh/garam-agent-operator/internal/garam/constructor"
)

// setAvailable puts the condition the reconciler writes on the Agent
// constructed for agent, which is what an observation reads.
func setAvailable(t *testing.T, c client.Client, name string, status metav1.ConditionStatus) {
	t.Helper()

	constructed := &agentv1alpha1.Agent{}
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: namespace, Name: name}, constructed); err != nil {
		t.Fatalf("read the agent to report on: %v", err)
	}
	constructed.Status.Conditions = []metav1.Condition{{
		Type:               agentv1alpha1.ConditionAvailable,
		Status:             status,
		Reason:             agentv1alpha1.ReasonReplicaReady,
		Message:            "what the reconciler observed",
		LastTransitionTime: metav1.Now(),
	}}
	if err := c.Status().Update(context.Background(), constructed); err != nil {
		t.Fatalf("report on the agent: %v", err)
	}
}

func TestObservationsReadTheEpochAndTheReadinessOffTheAgentTheyAreOn(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	building := newConstructor(t, scheme, c)

	g.Expect(building.Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	setAvailable(t, c, constructor.Name(sampleAgent), metav1.ConditionTrue)

	observations, err := building.Observations(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(observations).To(Equal([]garam.Observation{
		{Agent: sampleAgent, Epoch: sampleEpoch, Readiness: garam.ReadinessReplicaReady},
	}))
}

func TestObservationsReadNoReadyReplicaAsNoReplicaAndAnUnreadWorkloadAsUnobserved(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	building := newConstructor(t, scheme, c)

	g.Expect(building.Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	g.Expect(building.Construct(context.Background(), definitionOf(otherAgent), sampleEpoch, sampleCredential)).To(Succeed())
	setAvailable(t, c, constructor.Name(sampleAgent), metav1.ConditionFalse)
	setAvailable(t, c, constructor.Name(otherAgent), metav1.ConditionUnknown)

	observations, err := building.Observations(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(observations).To(ConsistOf(
		garam.Observation{Agent: sampleAgent, Epoch: sampleEpoch, Readiness: garam.ReadinessNoReplica},
		garam.Observation{Agent: otherAgent, Epoch: sampleEpoch, Readiness: garam.ReadinessUnobserved},
	))
}

// TestObservationsLeaveOutAnAgentThisOperatorHoldsNoRecordOf is what keeps a
// report off an Agent a user wrote. Such an Agent names no garam agent and
// carries no epoch, so there is nothing garam would accept a report at, and a
// guess would be a claim about an agent this operator was never assigned.
func TestObservationsLeaveOutAnAgentThisOperatorHoldsNoRecordOf(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	building := newConstructor(t, scheme, c)

	written := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "written-by-a-user", Namespace: namespace},
		Spec: agentv1alpha1.AgentSpec{
			Image:                 image,
			CredentialsSecretName: "written-by-a-user-credentials",
			StorageSize:           resource.MustParse(storageSize),
		},
	}
	g.Expect(c.Create(context.Background(), written)).To(Succeed())
	setAvailable(t, c, "written-by-a-user", metav1.ConditionTrue)

	// The constructed agent beside it, so that an empty answer cannot be read
	// as the user's Agent having been left out.
	g.Expect(building.Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	setAvailable(t, c, constructor.Name(sampleAgent), metav1.ConditionTrue)

	observations, err := building.Observations(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(observations).To(Equal([]garam.Observation{
		{Agent: sampleAgent, Epoch: sampleEpoch, Readiness: garam.ReadinessReplicaReady},
	}))
}

func TestObservationsReportAListItCouldNotRead(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	refusing := interceptor.NewClient(newClient(scheme), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errAPIRefusal
		},
	})

	_, err := newConstructor(t, scheme, refusing).Observations(context.Background())
	g.Expect(err).To(MatchError(errAPIRefusal))
}

// cutOver moves an Agent the constructor built to the control source, as the
// renderer does on a cutover (#217).
func cutOver(t *testing.T, c client.Client, agent garam.GRN) {
	t.Helper()

	built := &agentv1alpha1.Agent{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: constructor.Name(agent)}, built); err != nil {
		t.Fatal(err)
	}
	moved := built.DeepCopy()
	moved.Spec.Identity.Source = agentv1alpha1.DesiredSourceControl
	if err := c.Patch(context.Background(), moved, client.MergeFrom(built)); err != nil {
		t.Fatal(err)
	}
}

func TestObservationsLeaveOutAnAgentCutOverToTheControlService(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	building := newConstructor(t, scheme, c)

	g.Expect(building.Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	g.Expect(building.Construct(context.Background(), definitionOf(otherAgent), sampleEpoch, sampleCredential)).To(Succeed())
	setAvailable(t, c, constructor.Name(sampleAgent), metav1.ConditionTrue)
	setAvailable(t, c, constructor.Name(otherAgent), metav1.ConditionTrue)
	cutOver(t, c, otherAgent)

	observations, err := building.Observations(context.Background())
	g.Expect(err).NotTo(HaveOccurred())

	// The control: the garam-source agent beside it is still reported.
	g.Expect(observations).To(Equal([]garam.Observation{
		{Agent: sampleAgent, Epoch: sampleEpoch, Readiness: garam.ReadinessReplicaReady},
	}))
}

func TestHeldByControlReadsTheSourceOffTheAgentsSpec(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	building := newConstructor(t, scheme, c)
	g.Expect(building.Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	g.Expect(building.Construct(context.Background(), definitionOf(otherAgent), sampleEpoch, sampleCredential)).To(Succeed())
	cutOver(t, c, otherAgent)

	// The control: an Agent the constructor built is garam's.
	held, err := building.HeldByControl(context.Background(), sampleAgent)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(held).To(BeFalse())

	held, err = building.HeldByControl(context.Background(), otherAgent)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(held).To(BeTrue())

	held, err = building.HeldByControl(context.Background(), garam.GRN("grn:acme:default:agent:0000000000000000"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(held).To(BeFalse(), "no Agent is none of the control service's")
}

func TestCorrectSpecLeavesAnAgentCutOverToTheControlServiceAlone(t *testing.T) {
	g := NewWithT(t)
	scheme := newScheme(t)
	c := newClient(scheme)
	g.Expect(newConstructor(t, scheme, c).Construct(context.Background(), definitionOf(sampleAgent), sampleEpoch, sampleCredential)).To(Succeed())
	g.Expect(newConstructor(t, scheme, c).Construct(context.Background(), definitionOf(otherAgent), sampleEpoch, sampleCredential)).To(Succeed())
	cutOver(t, c, otherAgent)

	corrector := newCorrector(t, scheme, c)
	// The control: the garam-source agent's image is corrected.
	corrected, err := corrector.CorrectSpec(context.Background(), sampleAgent)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(corrected).To(BeTrue())

	corrected, err = corrector.CorrectSpec(context.Background(), otherAgent)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(corrected).To(BeFalse())
	g.Expect(imageOf(t, c, otherAgent)).To(Equal(image))
}
