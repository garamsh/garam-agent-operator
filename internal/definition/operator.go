package definition

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// maxOperatorImages bounds the images one report names.
const maxOperatorImages = 16

// imageDigest is the form of an image digest a report may name.
var imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ErrInvalidImages is returned for an image report that names no image, an image with no name or
// reference, one name twice, more images than one report carries, or a digest of another form.
var ErrInvalidImages = errors.New("the report is not a list of named images with references")

// OperatorImage is one image a controller runs: the reference it was configured with, and the
// digest where the controller knows one.
type OperatorImage struct {
	Name   string
	Ref    string
	Digest *string
}

// Operator is what this service holds of one controller: the images it last reported, and when it
// reported them and last reached this service. A nil time is one that never happened.
type Operator struct {
	GRN           string
	Images        []OperatorImage
	ReportedAt    *time.Time
	LastContactAt *time.Time
}

// RecordOperatorContact records that operator, held by the organization orgGRN, reached this
// service at at. A contact earlier than the one stored changes nothing.
func (s *service) RecordOperatorContact(ctx context.Context, orgGRN, operator string, at time.Time) error {
	return s.repository.RecordOperatorContact(ctx, orgGRN, operator, at)
}

// ReportOperatorImages records images as the ones operator, held by the organization orgGRN, runs
// as of at, replacing what it reported before.
func (s *service) ReportOperatorImages(ctx context.Context, orgGRN, operator string, images []OperatorImage, at time.Time) error {
	if len(images) == 0 || len(images) > maxOperatorImages {
		return ErrInvalidImages
	}
	names := map[string]bool{}
	for _, image := range images {
		if image.Name == "" || image.Ref == "" || names[image.Name] ||
			(image.Digest != nil && !imageDigest.MatchString(*image.Digest)) {
			return ErrInvalidImages
		}
		names[image.Name] = true
	}
	return s.repository.ReportOperatorImages(ctx, orgGRN, operator, images, at)
}

// ListOperators returns every controller the organization orgGRN holds that reached this service,
// ordered by GRN.
func (s *service) ListOperators(ctx context.Context, orgGRN string) ([]Operator, error) {
	return s.repository.ListOperators(ctx, orgGRN)
}
