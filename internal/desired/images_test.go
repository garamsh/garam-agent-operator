package desired

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// copyRef is the copy image every test configures.
const copyRef = "busybox:stable-musl"

// pinned is a digest of the form a reference pins and a runtime reports.
const pinned = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func digestPtr(s string) *string { return &s }

func TestConfiguredImagesLeaveOutAnUnsetFlagAndTakeADigestOnlyFromAPin(t *testing.T) {
	g := NewWithT(t)

	g.Expect(ConfiguredImages("registry.example/sherlock:1.2.0@"+pinned, "", copyRef, "registry.example/ws@sha256:short")).
		To(Equal([]Image{
			{Name: imageAgent, Ref: "registry.example/sherlock:1.2.0@" + pinned, Digest: digestPtr(pinned)},
			{Name: imageCopy, Ref: copyRef},
			{Name: imageWorkspace, Ref: "registry.example/ws@sha256:short"},
		}))
	g.Expect(ConfiguredImages("", "", "", "")).To(BeEmpty())
}

func TestDigestOfTakesOnlyASha256Digest(t *testing.T) {
	g := NewWithT(t)

	g.Expect(DigestOf("registry.example/manager@" + pinned)).To(Equal(digestPtr(pinned)))
	g.Expect(DigestOf("docker-pullable://registry.example/manager@" + pinned)).To(Equal(digestPtr(pinned)))
	g.Expect(DigestOf(pinned)).To(Equal(digestPtr(pinned)))
	g.Expect(DigestOf("")).To(BeNil())
	g.Expect(DigestOf("registry.example/manager:1.0")).To(BeNil())
	g.Expect(DigestOf("registry.example/manager@sha512:00")).To(BeNil())
}

// fixedManager answers the manager's own image, or err.
type fixedManager struct {
	image Image
	err   error
}

func (f fixedManager) ManagerImage(context.Context) (Image, error) { return f.image, f.err }

// imagesRoute answers each report with the next of answers, the last one from
// then on, and records every report's body.
type imagesRoute struct {
	mu      sync.Mutex
	answers []int
	reports []wireImages
}

func (r *imagesRoute) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body wireImages
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.Method != http.MethodPut || req.URL.Path != "/v1/operators/self/images" {
		w.WriteHeader(http.StatusNotFound)

		return
	}
	r.reports = append(r.reports, body)
	status := r.answers[min(len(r.reports), len(r.answers))-1]
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "not now"})
	}
}

func (r *imagesRoute) received() []wireImages {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]wireImages(nil), r.reports...)
}

// runReporter runs a reporter of manager and configured against a route
// answering answers in turn, with every wait a millisecond, and reports whether
// it returned before the test's deadline.
func runReporter(t *testing.T, manager ManagerImager, configured []Image, answers ...int) (*imagesRoute, bool) {
	t.Helper()

	route := &imagesRoute{answers: answers}
	server := httptest.NewTLSServer(route)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	reporter := NewImageReporter(NewClient(server.Listener.Addr().String(),
		&http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}), manager, configured)
	reporter.transientFirst, reporter.transientLast, reporter.refusedWait = time.Millisecond, time.Millisecond, time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g := NewWithT(t)
	g.Expect(reporter.Start(ctx)).To(Succeed())

	return route, ctx.Err() == nil
}

func TestImageReporterReportsTheManagersImageFirstAndStopsOnceAccepted(t *testing.T) {
	g := NewWithT(t)

	manager := Image{Name: "manager", Ref: "registry.example/operator:1.0", Digest: digestPtr(pinned)}
	route, returned := runReporter(t, fixedManager{image: manager},
		ConfiguredImages("", "", copyRef, ""), http.StatusNoContent)

	g.Expect(returned).To(BeTrue(), "the reporter returns once its report is accepted")
	g.Expect(route.received()).To(Equal([]wireImages{{Images: []wireImage{
		{Name: "manager", Ref: "registry.example/operator:1.0", Digest: digestPtr(pinned)},
		{Name: imageCopy, Ref: copyRef},
	}}}))
}

func TestImageReporterReportsWithoutTheManagersImageWhereItCannotBeRead(t *testing.T) {
	g := NewWithT(t)

	route, returned := runReporter(t, fixedManager{err: errors.New("no Pod")},
		ConfiguredImages("", "", copyRef, ""), http.StatusNoContent)

	g.Expect(returned).To(BeTrue())
	g.Expect(route.received()).To(Equal([]wireImages{{Images: []wireImage{{Name: imageCopy, Ref: copyRef}}}}))
}

func TestImageReporterReportsAgainAfterAFailureOrARefusalUntilAccepted(t *testing.T) {
	g := NewWithT(t)

	route, returned := runReporter(t, fixedManager{err: errors.New("no Pod")},
		ConfiguredImages("", "", copyRef, ""),
		http.StatusServiceUnavailable, http.StatusForbidden, http.StatusNoContent)

	g.Expect(returned).To(BeTrue())
	g.Expect(route.received()).To(HaveLen(3), "a 503 and a 403 are each asked again, and the 204 ends it")
}

func TestImageReporterSendsNothingAndStopsWhereThereIsNoImageToReport(t *testing.T) {
	g := NewWithT(t)

	route, returned := runReporter(t, fixedManager{err: errors.New("no Pod")}, nil, http.StatusBadRequest)

	g.Expect(returned).To(BeTrue(), "the reporter returns rather than sending a report the service refuses")
	g.Expect(route.received()).To(BeEmpty())
}
