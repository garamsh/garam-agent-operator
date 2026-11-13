package gaonfake_test

import (
	"testing"

	"github.com/garamsh/gaon/lib/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/tests/gaon/gaonfake"
)

func TestWriteStatus_SetsWhatAReadAnswers(t *testing.T) {
	fake, client := gaonfake.New(t, nil)
	_, err := client.PutMicroVM(t.Context(), &api.MicroVM{
		TypeMeta: api.TypeMeta{APIVersion: api.GroupVersion, Kind: api.KindMicroVM},
		Metadata: api.ObjectMeta{Name: "vm"},
		Spec:     api.MicroVMSpec{Image: "guest:1", Command: []string{"/run"}, RunStrategy: api.RunAlways},
	})
	require.NoError(t, err)
	status := api.MicroVMStatus{
		Phase:      api.MicroVMRunning,
		Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionTrue}},
	}

	require.NoError(t, fake.WriteStatus("vm", status))

	m, ok := fake.MicroVM(t, "vm")
	require.True(t, ok)
	assert.Equal(t, api.MicroVMRunning, m.EffectivePhase())
	assert.Equal(t, status.Conditions, m.Status.Conditions)
}

func TestWriteStatus_RefusesAMicroVMThatDoesNotExist(t *testing.T) {
	fake, _ := gaonfake.New(t, nil)

	err := fake.WriteStatus("absent", api.MicroVMStatus{Phase: api.MicroVMRunning})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent")
}
