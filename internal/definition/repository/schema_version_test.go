package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSchemaVerdict_RefusesEverySchemaButTheOneKnown(t *testing.T) {
	const known = 3
	refused := map[string]struct {
		record schemaRecord
		says   string
	}{
		"no version recorded": {schemaRecord{}, "has not migrated it yet"},
		"an older version":    {schemaRecord{recorded: true, version: 2}, "has not migrated it yet"},
		"a newer version":     {schemaRecord{recorded: true, version: 4}, "a newer control service migrated it"},
		"dirty at the known":  {schemaRecord{recorded: true, version: 3, dirty: true}, "dirty at schema version 3"},
	}
	for name, tt := range refused {
		t.Run(name, func(t *testing.T) {
			err := schemaVerdict(tt.record, known)
			assert.ErrorIs(t, err, ErrSchemaNotCurrent)
			assert.ErrorContains(t, err, tt.says)
		})
	}

	// Control: the version known, clean, is current.
	assert.NoError(t, schemaVerdict(schemaRecord{recorded: true, version: known}, known))
}
