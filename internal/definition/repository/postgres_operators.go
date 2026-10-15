package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// imageColumn is one reported image as stored in operators.images.
type imageColumn struct {
	Name   string  `json:"name"`
	Ref    string  `json:"ref"`
	Digest *string `json:"digest,omitempty"`
}

func (p *Postgres) RecordOperatorContact(ctx context.Context, orgGRN, operator string, at time.Time) error {
	if _, err := p.pool.Exec(ctx, recordOperatorContact, operator, orgGRN, at); err != nil {
		return storeError("record operator contact", err)
	}
	return nil
}

func (p *Postgres) ReportOperatorImages(
	ctx context.Context, orgGRN, operator string, images []definition.OperatorImage, at time.Time,
) error {
	stored := make([]imageColumn, 0, len(images))
	for _, image := range images {
		stored = append(stored, imageColumn{Name: image.Name, Ref: image.Ref, Digest: image.Digest})
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return storeError("encode operator images", err)
	}
	if _, err := p.pool.Exec(ctx, reportOperatorImages, operator, orgGRN, encoded, at); err != nil {
		return storeError("report operator images", err)
	}
	return nil
}

func (p *Postgres) ListOperators(ctx context.Context, orgGRN string) ([]definition.Operator, error) {
	rows, err := p.pool.Query(ctx, listOperators, orgGRN)
	if err != nil {
		return nil, storeError("list operators", err)
	}
	defer rows.Close()
	out := []definition.Operator{}
	for rows.Next() {
		var (
			o       definition.Operator
			encoded []byte
		)
		if err := rows.Scan(&o.GRN, &encoded, &o.ReportedAt, &o.LastContactAt); err != nil {
			return nil, storeError("list operators", err)
		}
		var images []imageColumn
		if encoded != nil {
			if err := json.Unmarshal(encoded, &images); err != nil {
				return nil, storeError("decode operator images", err)
			}
		}
		for _, image := range images {
			o.Images = append(o.Images, definition.OperatorImage{Name: image.Name, Ref: image.Ref, Digest: image.Digest})
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list operators", err)
	}
	return out, nil
}
