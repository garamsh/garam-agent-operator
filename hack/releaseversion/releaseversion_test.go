package releaseversion_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/hack/releaseversion"
)

var accepted = []string{
	"0.1.0-dev.1", "0.1.0-dev.5", "0.1.0-dev.10", "0.1.0-dev.123", "0.1.0-rc.1", "0.1.0-rc.12", "0.1.0",
}

// refused holds names one edit from an accepted one, and the forms the scheme replaced.
var refused = []string{
	"0.2.0", "1.0.0", "0.1.1", "0.0.1", "0.1",
	"0.1.0-dev.0", "0.1.0-dev.01", "0.1.0-rc.0", "0.1.0-rc.01",
	"0.1.0-dev", "0.1.0-dev.", "0.1.0-dev.-1", "0.1.0-dev.1.2",
	"0.1.0-rc", "0.1.0-beta.1", "0.1.0-alpha.1", "0.1.0-DEV.1", "0.1.0-dev1",
	"0.1.0-dev.1-x", "0.1.0-rc.1-x", "0.1.0-dev.1+build", "0.1.0+build",
	"0.1.0-dev.1\n", "0.1.0\n", "v0.1.0", " 0.1.0", "0.1.0 ", "00.1.0", "0.01.0", "0.1.00", "",
}

func TestValid_AcceptsTheThreeFormsAndNothingElse(t *testing.T) {
	for _, version := range accepted {
		assert.True(t, releaseversion.Valid(version), version)
	}
	for _, version := range refused {
		assert.False(t, releaseversion.Valid(version), "%q", version)
	}
}

func TestFromTag_StripsTheVOfAnAcceptedTag(t *testing.T) {
	for _, version := range accepted {
		got, err := releaseversion.FromTag("v" + version)
		require.NoError(t, err, version)
		assert.Equal(t, version, got)
	}
}

func TestFromTag_RefusesEveryOtherTag(t *testing.T) {
	for _, version := range refused {
		_, err := releaseversion.FromTag("v" + version)
		require.Error(t, err, "v%q", version)
		assert.Contains(t, err.Error(), "ADR 0081")
	}

	// Controls: the accepted forms are refused without their v, and a doubled v is refused, so the
	// verdict comes from the form and from exactly one v.
	for _, version := range accepted {
		_, err := releaseversion.FromTag(version)
		assert.Error(t, err, "an accepted version without its v: %s", version)
	}
	_, err := releaseversion.FromTag("vv0.1.0")
	assert.Error(t, err)
}
