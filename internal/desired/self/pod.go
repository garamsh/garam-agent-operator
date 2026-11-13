// Package self reads the image the manager runs off its own Pod, for the image
// report to the control service (ADR 0074).
package self

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/garamsh/garam-agent-operator/internal/desired"
)

// container is the name config/manager gives the manager's container.
const container = "manager"

// imageName is the name the manager's own image is reported under.
const imageName = "manager"

// ErrNoPodName is returned where the manager was not told its Pod's name.
var ErrNoPodName = errors.New("the manager's Pod name is unset")

// Pod implements desired.ManagerImager on the manager's own Pod.
type Pod struct {
	reader          client.Reader
	namespace, name string
}

// NewPod returns a Pod reading the Pod name in namespace through reader, which
// reads straight from the API server: the manager's cache holds only the Pods
// it builds.
func NewPod(reader client.Reader, namespace, name string) *Pod {
	return &Pod{reader: reader, namespace: namespace, name: name}
}

// ManagerImage implements desired.ManagerImager. The reference is the manager
// container's image as its spec names it, and the digest the one its status
// reports it running, where that is a sha256 digest.
func (p *Pod) ManagerImage(ctx context.Context) (desired.Image, error) {
	if p.name == "" {
		return desired.Image{}, ErrNoPodName
	}
	var pod corev1.Pod
	if err := p.reader.Get(ctx, client.ObjectKey{Namespace: p.namespace, Name: p.name}, &pod); err != nil {
		return desired.Image{}, fmt.Errorf("read the manager's Pod %s/%s: %w", p.namespace, p.name, err)
	}
	for _, c := range pod.Spec.Containers {
		if c.Name != container {
			continue
		}
		image := desired.Image{Name: imageName, Ref: c.Image}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == container {
				image.Digest = desired.DigestOf(status.ImageID)
			}
		}

		return image, nil
	}

	return desired.Image{}, fmt.Errorf("the manager's Pod %s/%s has no %q container", p.namespace, p.name, container)
}
