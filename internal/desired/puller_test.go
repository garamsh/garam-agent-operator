package desired

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// valueOf reads a gauge's or a counter's current value.
func valueOf(metric prometheus.Metric) float64 {
	var read dto.Metric
	if err := metric.Write(&read); err != nil {
		panic(err)
	}
	if read.Gauge != nil {
		return read.Gauge.GetValue()
	}

	return read.Counter.GetValue()
}

// The tests are in the package so that they can shorten the puller's waits,
// which is the only thing they reach that a caller cannot.

// feedDouble stands in for the control service's controller routes: it answers
// the desired feed from a script, one entry per request, and records every
// status reported. A request past the end of the script waits until the test
// ends, as a long poll on a feed that never moves would.
type feedDouble struct {
	mu       sync.Mutex
	script   []feedReply
	requests []*http.Request
	statuses []string
	done     chan struct{}
}

// feedReply is one scripted answer: a status and its body.
type feedReply struct {
	status int
	body   string
}

func (f *feedDouble) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/status") {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.statuses = append(f.statuses, strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/status"),
			"/v1/operators/self/agents/")+" "+string(body))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))

		return
	}

	f.mu.Lock()
	f.requests = append(f.requests, r)
	if len(f.script) == 0 {
		f.mu.Unlock()
		select {
		case <-f.done:
		case <-r.Context().Done():
		}

		return
	}
	reply := f.script[0]
	f.script = f.script[1:]
	f.mu.Unlock()

	w.WriteHeader(reply.status)
	_, _ = w.Write([]byte(reply.body))
}

// requestCount is how many times the desired feed was asked.
func (f *feedDouble) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.requests)
}

// reported is every status reported, as "<agent> <body>".
func (f *feedDouble) reported() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.statuses...)
}

// recordingRenderer records every revision rendered, as "<grn>@<revision>".
type recordingRenderer struct {
	mu       sync.Mutex
	rendered []string
	refuse   map[string]error
}

func (r *recordingRenderer) Render(_ context.Context, agent Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refuse[agent.GRN]; err != nil {
		return err
	}
	r.rendered = append(r.rendered, agent.GRN+"@"+agent.Revision)

	return nil
}

func (r *recordingRenderer) renders() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.rendered...)
}

// startPuller serves script from a TLS feed double and runs a puller against it
// until the test ends, with its transient wait shortened to a millisecond and
// its wait after a refusal to refused.
func startPuller(t *testing.T, renderer Renderer, refused time.Duration, script ...feedReply) *feedDouble {
	t.Helper()

	feed := &feedDouble{script: script, done: make(chan struct{})}
	server := httptest.NewTLSServer(feed)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	puller := NewPuller(NewClient(server.Listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), renderer)
	puller.transientFirst, puller.transientLast, puller.refusedWait = time.Millisecond, time.Millisecond, refused

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		_ = puller.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		close(feed.done)
		<-stopped
		server.Close()
	})

	return feed
}

// answer is the desired feed's body for a cursor and agents, each given as
// "<grn>@<revision>".
func answer(cursor string, agents ...string) feedReply {
	type agent struct {
		Agent    string `json:"agent"`
		Revision string `json:"revision"`
		Epoch    string `json:"epoch"`
	}
	body := struct {
		Cursor string  `json:"cursor"`
		Agents []agent `json:"agents"`
	}{Cursor: cursor, Agents: []agent{}}
	for _, a := range agents {
		grn, revision, _ := strings.Cut(a, "@")
		body.Agents = append(body.Agents, agent{Agent: grn, Revision: revision, Epoch: "7"})
	}
	raw, _ := json.Marshal(body)

	return feedReply{status: http.StatusOK, body: string(raw)}
}

const (
	agentA = "grn:acme:default:agent:aaaaaaaaaaaaaaaa"
	agentB = "grn:acme:default:agent:bbbbbbbbbbbbbbbb"
)

func TestPullerRendersEveryNewRevisionOnceAndReportsIt(t *testing.T) {
	g := NewWithT(t)
	renderer := &recordingRenderer{}

	// The second answer repeats revision 1, which is the control: an unchanged
	// revision is not rendered or reported again, so the third's 2 is new.
	feed := startPuller(t, renderer, time.Hour,
		answer("1", agentA+"@1"), answer("2", agentA+"@1"), answer("3", agentA+"@2"))

	g.Eventually(renderer.renders).Should(Equal([]string{agentA + "@1", agentA + "@2"}))
	g.Eventually(feed.reported).Should(Equal([]string{
		agentA + ` {"observedRevision":"1","renderedRevision":"1"}`,
		agentA + ` {"observedRevision":"2","renderedRevision":"2"}`,
	}))

	By := "the first request asks at once, and each later one after the cursor it was given"
	g.Eventually(feed.requestCount).Should(BeNumerically(">=", 4), By)
	g.Expect(feed.requests[0].URL.Query().Has("after")).To(BeFalse(), By)
	g.Expect(feed.requests[0].URL.Query().Get("waitSeconds")).To(Equal("0"), By)
	g.Expect(feed.requests[3].URL.Query().Get("after")).To(Equal("3"), By)
	g.Expect(feed.requests[3].URL.Query().Get("waitSeconds")).To(Equal("30"), By)
}

func TestPullerLeavesAWithheldAgentAloneAndRendersTheRest(t *testing.T) {
	g := NewWithT(t)
	renderer := &recordingRenderer{}

	// The control is B, rendered from the answer that withholds A.
	startPuller(t, renderer, time.Hour,
		answer("1", agentA+"@1"), answer("2", agentB+"@1"))

	g.Eventually(renderer.renders).Should(Equal([]string{agentA + "@1", agentB + "@1"}))
	g.Consistently(renderer.renders, 200*time.Millisecond).Should(Equal([]string{agentA + "@1", agentB + "@1"}))
}

func TestPullerAsksAgainSoonAfterA503AndSlowlyAfterA422(t *testing.T) {
	g := NewWithT(t)

	By := "the control: a 503 is asked again after the short wait, and its answer rendered"
	unavailable := &recordingRenderer{}
	feed := startPuller(t, unavailable, time.Hour,
		feedReply{status: http.StatusServiceUnavailable, body: `{"kind":"undecided","message":"later"}`},
		answer("1", agentA+"@1"))
	g.Eventually(unavailable.renders).Should(Equal([]string{agentA + "@1"}), By)
	g.Expect(valueOf(feedRefused)).To(BeZero(), By)

	By = "a 422 is surfaced, counted, and not asked again before the long wait"
	before := valueOf(refusalsTotal.WithLabelValues(routeDesired, "422"))
	refused := &recordingRenderer{}
	refusedFeed := startPuller(t, refused, time.Hour,
		feedReply{status: http.StatusUnprocessableEntity, body: `{"kind":"too_many_agents","message":"over 500"}`},
		answer("1", agentA+"@1"))
	g.Eventually(refusedFeed.requestCount).Should(Equal(1), By)
	g.Eventually(func() float64 { return valueOf(feedRefused) }).Should(Equal(1.0), By)
	g.Expect(valueOf(refusalsTotal.WithLabelValues(routeDesired, "422"))).To(Equal(before+1), By)
	g.Consistently(refusedFeed.requestCount, 300*time.Millisecond).Should(Equal(1), By)
	g.Expect(refused.renders()).To(BeEmpty(), By)
	g.Expect(feed.requestCount()).To(BeNumerically(">=", 2))
}

func TestPullerKeepsAskingAfterARefusalAndClearsTheGaugeWhenItClears(t *testing.T) {
	g := NewWithT(t)
	renderer := &recordingRenderer{}

	startPuller(t, renderer, 10*time.Millisecond,
		feedReply{status: http.StatusForbidden, body: `{"message":"not authorized"}`},
		answer("1", agentA+"@1"))

	g.Eventually(renderer.renders).Should(Equal([]string{agentA + "@1"}))
	g.Expect(valueOf(feedRefused)).To(BeZero())
}

func TestPullerReportsNothingForARevisionItDidNotRender(t *testing.T) {
	g := NewWithT(t)
	renderer := &recordingRenderer{refuse: map[string]error{agentA: ErrNotControlSource}}

	// The control is B, rendered and reported from the same answer.
	feed := startPuller(t, renderer, time.Hour, answer("1", agentA+"@1", agentB+"@1"))

	g.Eventually(feed.reported).Should(Equal([]string{agentB + ` {"observedRevision":"1","renderedRevision":"1"}`}))
	g.Consistently(feed.reported, 200*time.Millisecond).Should(HaveLen(1))
}
