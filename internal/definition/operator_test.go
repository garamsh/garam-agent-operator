package definition_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/definition/repository"
)

const (
	operatorOrg = "grn:root:default:org:acme"
	operatorGRN = "grn:root:default:operator:k8s"
)

const (
	copyImage = "copy"
	busybox1  = "busybox:1"
	busybox2  = "busybox:2"
)

var operatorTime = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func TestReportOperatorImages_RefusesAReportThatIsNotOne(t *testing.T) {
	ctx := context.Background()
	bad := "sha256:XYZ"
	many := make([]definition.OperatorImage, 17)
	for i := range many {
		many[i] = definition.OperatorImage{Name: fmt.Sprintf("image-%d", i), Ref: busybox1}
	}
	for _, tc := range []struct {
		name   string
		images []definition.OperatorImage
	}{
		{"none", nil},
		{"more than one report carries", many},
		{"no name", []definition.OperatorImage{{Ref: busybox1}}},
		{"no reference", []definition.OperatorImage{{Name: copyImage}}},
		{"one name twice", []definition.OperatorImage{{Name: copyImage, Ref: busybox1}, {Name: copyImage, Ref: busybox2}}},
		{"a digest of another form", []definition.OperatorImage{{Name: copyImage, Ref: busybox1, Digest: &bad}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := definition.NewService(repository.NewMemory(), nil)
			err := service.ReportOperatorImages(ctx, operatorOrg, operatorGRN, tc.images, operatorTime)
			require.ErrorIs(t, err, definition.ErrInvalidImages)
			operators, err := service.ListOperators(ctx, operatorOrg)
			require.NoError(t, err)
			assert.Empty(t, operators)

			// Control: sixteen distinct images, one with a digest, are one report.
			good := "sha256:" + fmt.Sprintf("%064x", 1)
			images := append([]definition.OperatorImage(nil), many[:16]...)
			images[0].Digest = &good
			require.NoError(t, service.ReportOperatorImages(ctx, operatorOrg, operatorGRN, images, operatorTime))
		})
	}
}

func TestOperators_KeepTheLatestContactAndReportApart(t *testing.T) {
	ctx := context.Background()
	service := definition.NewService(repository.NewMemory(), nil)
	later := operatorTime.Add(time.Minute)

	require.NoError(t, service.RecordOperatorContact(ctx, operatorOrg, operatorGRN, later))
	operators, err := service.ListOperators(ctx, operatorOrg)
	require.NoError(t, err)
	require.Len(t, operators, 1)
	assert.Nil(t, operators[0].ReportedAt, "a contact is no report")
	assert.Empty(t, operators[0].Images)

	// A contact arriving late lowers nothing.
	require.NoError(t, service.RecordOperatorContact(ctx, operatorOrg, operatorGRN, operatorTime))
	// A report arriving late replaces nothing.
	require.NoError(t, service.ReportOperatorImages(ctx, operatorOrg, operatorGRN,
		[]definition.OperatorImage{{Name: copyImage, Ref: busybox2}}, later))
	require.NoError(t, service.ReportOperatorImages(ctx, operatorOrg, operatorGRN,
		[]definition.OperatorImage{{Name: copyImage, Ref: busybox1}}, operatorTime))

	operators, err = service.ListOperators(ctx, operatorOrg)
	require.NoError(t, err)
	require.Len(t, operators, 1)
	assert.Equal(t, later, *operators[0].LastContactAt)
	assert.Equal(t, later, *operators[0].ReportedAt)
	assert.Equal(t, []definition.OperatorImage{{Name: copyImage, Ref: busybox2}}, operators[0].Images)

	// Another organization holds none of them.
	others, err := service.ListOperators(ctx, "grn:root:default:org:globex")
	require.NoError(t, err)
	assert.Empty(t, others)
}
