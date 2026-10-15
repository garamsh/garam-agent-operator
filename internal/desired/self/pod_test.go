package self_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/garamsh/garam-agent-operator/internal/desired"
	"github.com/garamsh/garam-agent-operator/internal/desired/self"
)

const (
	// managerName is the manager container's name, and the name its image is reported under.
	managerName = "manager"
	managerRef  = "registry.example/operator:1.0"
	digest      = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var namespaces int

// managerPod creates a namespace of the spec's own holding a Pod named name
// whose manager container runs managerRef, and records imageID as what the
// kubelet reports it running. It returns the namespace.
func managerPod(name, imageID string) string {
	namespaces++
	namespace := fmt.Sprintf("self-%d", namespaces)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: managerName, Image: managerRef},
		}},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: managerName, Image: managerRef, ImageID: imageID}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

	// The setup is what the spec asks for: the status the kubelet would have written.
	var stored corev1.Pod
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &stored)).To(Succeed())
	Expect(stored.Status.ContainerStatuses).To(HaveLen(1))
	Expect(stored.Status.ContainerStatuses[0].ImageID).To(Equal(imageID))

	return namespace
}

var _ = Describe("The manager's own image", func() {
	It("is the manager container's reference, with the digest the kubelet reports it running", func() {
		namespace := managerPod("manager-0", "registry.example/operator@"+digest)

		image, err := self.NewPod(k8sClient, namespace, "manager-0").ManagerImage(ctx)
		Expect(err).NotTo(HaveOccurred())
		pinned := digest
		Expect(image).To(Equal(desired.Image{Name: managerName, Ref: managerRef, Digest: &pinned}))
	})

	It("carries no digest where the kubelet reports none of sha256's form", func() {
		namespace := managerPod("manager-0", "")

		image, err := self.NewPod(k8sClient, namespace, "manager-0").ManagerImage(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(image).To(Equal(desired.Image{Name: managerName, Ref: managerRef}))
	})

	It("is not read where the Pod's name is unset, or the Pod cannot be read", func() {
		namespace := managerPod("manager-0", "registry.example/operator@"+digest)

		_, err := self.NewPod(k8sClient, namespace, "").ManagerImage(ctx)
		Expect(err).To(MatchError(self.ErrNoPodName))

		_, err = self.NewPod(k8sClient, namespace, "manager-1").ManagerImage(ctx)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "%v", err)

		// Control: the Pod the manager runs in is read.
		_, err = self.NewPod(k8sClient, namespace, "manager-0").ManagerImage(ctx)
		Expect(err).NotTo(HaveOccurred())
	})
})
