package repository

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// storedOperator is an operator as Memory holds it, under the organization holding it.
type storedOperator struct {
	orgGRN   string
	operator definition.Operator
}

func (m *Memory) RecordOperatorContact(_ context.Context, orgGRN, operator string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.operatorLocked(orgGRN, operator)
	if stored.operator.LastContactAt == nil || stored.operator.LastContactAt.Before(at) {
		stored.operator.LastContactAt = &at
	}
	m.operators[operator] = stored
	return nil
}

func (m *Memory) ReportOperatorImages(
	_ context.Context, orgGRN, operator string, images []definition.OperatorImage, at time.Time,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.operatorLocked(orgGRN, operator)
	if stored.operator.ReportedAt == nil || !stored.operator.ReportedAt.After(at) {
		stored.operator.Images, stored.operator.ReportedAt = cloneImages(images), &at
	}
	m.operators[operator] = stored
	return nil
}

func (m *Memory) ListOperators(_ context.Context, orgGRN string) ([]definition.Operator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []definition.Operator{}
	for _, stored := range m.operators {
		if stored.orgGRN == orgGRN {
			o := stored.operator
			o.Images = cloneImages(o.Images)
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b definition.Operator) int { return strings.Compare(a.GRN, b.GRN) })
	return out, nil
}

// operatorLocked returns operator as stored, or a new one held by orgGRN.
func (m *Memory) operatorLocked(orgGRN, operator string) storedOperator {
	stored, ok := m.operators[operator]
	if !ok {
		stored = storedOperator{operator: definition.Operator{GRN: operator}}
	}
	stored.orgGRN = orgGRN
	return stored
}

// cloneImages is a copy of images sharing nothing with it.
func cloneImages(images []definition.OperatorImage) []definition.OperatorImage {
	out := make([]definition.OperatorImage, 0, len(images))
	for _, image := range images {
		if image.Digest != nil {
			digest := *image.Digest
			image.Digest = &digest
		}
		out = append(out, image)
	}
	return out
}
