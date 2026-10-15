package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/garamsh/garam-agent-operator/internal/definition"
)

// maxImagesBytes bounds an image report's body.
const maxImagesBytes = 16 << 10

// imagesRequest is a controller's report of the images it runs (ADR 0074).
type imagesRequest struct {
	Images []wireImage `json:"images"`
}

// wireImage is one image: the reference the controller was configured with, and its digest where
// the controller knows one.
type wireImage struct {
	Name   string  `json:"name"`
	Ref    string  `json:"ref"`
	Digest *string `json:"digest"`
}

// errInvalidImagesBody is returned for a body that is not one image report.
var errInvalidImagesBody = errors.New("request body is not an image report")

// reportImages records the images the calling controller reports, replacing its earlier report.
// It answers 204.
func (s *server) reportImages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImagesBytes))
	if err != nil {
		s.respondError(w, errInvalidImagesBody)
		return
	}
	c, err := s.authenticate(r.Context(), r)
	if err != nil {
		s.respondError(w, err)
		return
	}
	var in imagesRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.More() {
		s.respondError(w, errInvalidImagesBody)
		return
	}
	images := make([]definition.OperatorImage, 0, len(in.Images))
	for _, image := range in.Images {
		images = append(images, definition.OperatorImage{Name: image.Name, Ref: image.Ref, Digest: image.Digest})
	}
	if err := s.definitions.ReportOperatorImages(r.Context(), c.orgGRN, c.grn, images, s.now()); err != nil {
		s.respondError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
