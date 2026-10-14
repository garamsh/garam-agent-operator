package desired

import (
	"context"
	"regexp"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The names a configured image is reported under.
const (
	imageAgent     = "agent"
	imageAdapter   = "adapter"
	imageCopy      = "copy"
	imageWorkspace = "workspace"
)

// sha256Digest is the form of an image digest this operator reports.
var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Image is one image this operator runs, as it reports it to the control
// service (ADR 0074): the reference it was configured with, and the digest
// where it knows one.
type Image struct {
	Name   string
	Ref    string
	Digest *string
}

// ConfiguredImages are the images the manager's flags configure, under the
// names they are reported by. An image whose flag is unset is not one this
// operator runs, so it is left out.
func ConfiguredImages(agent, adapter, copyImage, workspace string) []Image {
	var images []Image
	for _, configured := range []struct{ name, ref string }{
		{imageAgent, agent}, {imageAdapter, adapter}, {imageCopy, copyImage}, {imageWorkspace, workspace},
	} {
		if configured.ref != "" {
			images = append(images, configuredImage(configured.name, configured.ref))
		}
	}

	return images
}

// configuredImage is the image named name that ref configures. Its digest is
// the one ref pins with @sha256:, and none where ref pins none.
func configuredImage(name, ref string) Image {
	image := Image{Name: name, Ref: ref}
	if _, pinned, found := strings.Cut(ref, "@"); found && sha256Digest.MatchString(pinned) {
		image.Digest = &pinned
	}

	return image
}

// DigestOf is the sha256 digest an image ID ends in, as a container runtime
// reports one (`<repository>@sha256:<hex>` or `sha256:<hex>`), and nil where it
// ends in none.
func DigestOf(imageID string) *string {
	digest := imageID
	if i := strings.LastIndex(imageID, "@"); i >= 0 {
		digest = imageID[i+1:]
	}
	if !sha256Digest.MatchString(digest) {
		return nil
	}

	return &digest
}

// ManagerImager reads the image the manager itself runs.
type ManagerImager interface {
	ManagerImage(ctx context.Context) (Image, error)
}

// ImageReporter reports the images this operator runs to the control service
// once, after the manager starts, asking again until the report is accepted. It
// is a manager Runnable.
type ImageReporter struct {
	client     *Client
	manager    ManagerImager
	configured []Image

	// The waits, held here so a test in the package can shorten them.
	transientFirst, transientLast, refusedWait time.Duration
}

// NewImageReporter returns an ImageReporter reporting through client the
// manager's own image, read from manager, and configured.
func NewImageReporter(client *Client, manager ManagerImager, configured []Image) *ImageReporter {
	return &ImageReporter{
		client: client, manager: manager, configured: configured,
		transientFirst: transientFirst, transientLast: transientLast, refusedWait: refusedWait,
	}
}

// Start reports until one report is accepted or ctx is cancelled. It returns no
// error, because an error from a Runnable stops the manager.
func (r *ImageReporter) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("desired")
	var backoff time.Duration
	omitted := false
	for ctx.Err() == nil {
		images := r.configured
		own, err := r.manager.ManagerImage(ctx)
		switch {
		case err == nil:
			images = append([]Image{own}, r.configured...)
		case !omitted:
			omitted = true
			log.Error(err, "Reporting the images this operator runs without the manager's own")
		}

		err = r.client.ReportImages(ctx, images)
		if err == nil {
			log.Info("Reported the images this operator runs", "images", len(images))

			return nil
		}
		if refusal, refused := asRefusal(err); refused {
			backoff = 0
			log.Error(refusal, "The control service refused the image report; reporting again slowly",
				"status", refusal.Status, "kind", refusal.Kind, "wait", r.refusedWait)
			pause(ctx, r.refusedWait)

			continue
		}
		if backoff == 0 {
			backoff = r.transientFirst
		} else {
			backoff = min(2*backoff, r.transientLast)
		}
		log.Info("The image report did not answer; reporting again", "error", err.Error(), "wait", backoff)
		pause(ctx, backoff)
	}

	return nil
}
