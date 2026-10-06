package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ignore file's lines before and after #301, in order, as ignorefile.ReadAll returns them.
var (
	before = []string{"**", "!**/*.go", "**/*_test.go", "!internal/definition/repository/schema.sql", "!go.mod", "!go.sum"}
	after  = []string{"**", "!**/*.go", "**/*_test.go", "!internal/definition/repository/migrations/*.up.sql", "!go.mod", "!go.sum"}
)

func TestExcluded_NamesAnEmbeddedFileTheIgnoreFileLeavesOut(t *testing.T) {
	migration := embedded{path: "internal/definition/repository/migrations/000001_baseline.up.sql", by: "repository"}
	source := embedded{path: "internal/definition/repository/migrate.go", by: "repository"}

	missing, err := excluded(before, []embedded{migration, source})
	require.NoError(t, err)
	assert.Equal(t, []embedded{migration}, missing)

	// Control: the line #301 adds re-includes it, and the source stays in.
	missing, err = excluded(after, []embedded{migration, source})
	require.NoError(t, err)
	assert.Empty(t, missing)
}
