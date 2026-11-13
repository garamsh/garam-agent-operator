package keyfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/gaon/keyfile"
)

func write(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "k")
	require.NoError(t, os.WriteFile(path, []byte("secret-contents"), 0o600))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestReadsAFileOnlyItsOwnerCanReach(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		got, err := keyfile.Read(write(t, mode))
		require.NoError(t, err)
		assert.Equal(t, "secret-contents", string(got))
	}
}

func TestRefusesAFileOthersCanReach(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o601} {
		path := write(t, mode)
		_, err := keyfile.Read(path)
		require.Error(t, err, "%o", mode)
		assert.Contains(t, err.Error(), path)
		assert.NotContains(t, err.Error(), "secret-contents")
	}
}

func TestRefusesAMissingFile(t *testing.T) {
	_, err := keyfile.Read(filepath.Join(t.TempDir(), "absent"))
	require.Error(t, err)
}

// The refusal of a file owned by another user is not tried: a test
// cannot give a file to another user without root.
