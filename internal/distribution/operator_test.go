package distribution_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/definition"
	"github.com/garamsh/garam-agent-operator/internal/distribution"
)

// clock is the time the routes read, moved only by the test.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// imageDigest is an image digest of the form a report may name.
const imageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// reportImages sends body as the controller's image report and returns the status.
func (e *env) reportImages(t *testing.T, client *http.Client, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, e.server.URL+"/v1/operators/self/images", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// operatorOf is what the store holds of the controller in the env's organization, failing where nothing.
func (e *env) operatorOf(t *testing.T) definition.Operator {
	t.Helper()
	operators, err := e.definitions.ListOperators(context.Background(), org)
	require.NoError(t, err)
	for _, o := range operators {
		if o.GRN == controller {
			return o
		}
	}
	require.Failf(t, "operator not stored", "%s in %s", controller, org)
	return definition.Operator{}
}

func TestImages_ReportIsStoredForTheControllerInItsOrganizationAtTheTimeItArrived(t *testing.T) {
	e := newEnv(t)
	reportedAt := e.clock.Now()
	status := e.reportImages(t, e.withToken, `{"images": [
		{"name": "agent", "ref": "registry.example/sherlock:1.2.0@`+imageDigest+`", "digest": "`+imageDigest+`"},
		{"name": "copy", "ref": "busybox:stable-musl"}]}`)
	require.Equal(t, http.StatusNoContent, status)

	o := e.operatorOf(t)
	sha := imageDigest
	assert.Equal(t, []definition.OperatorImage{
		{Name: "agent", Ref: "registry.example/sherlock:1.2.0@" + imageDigest, Digest: &sha},
		{Name: "copy", Ref: "busybox:stable-musl"},
	}, o.Images)
	require.NotNil(t, o.ReportedAt)
	assert.True(t, reportedAt.Equal(*o.ReportedAt))

	// A later report replaces the first whole.
	e.clock.advance(time.Hour)
	require.Equal(t, http.StatusNoContent, e.reportImages(t, e.withToken, `{"images": [{"name": "copy", "ref": "busybox:1"}]}`))
	o = e.operatorOf(t)
	assert.Equal(t, []definition.OperatorImage{{Name: "copy", Ref: "busybox:1"}}, o.Images)
	assert.True(t, reportedAt.Add(time.Hour).Equal(*o.ReportedAt))
}

func TestImages_ReportThatIsNotOneIsRefusedAndStoresNothing(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no image", `{"images": []}`},
		{"no images field", `{}`},
		{"an image without a name", `{"images": [{"name": "", "ref": "busybox:1"}]}`},
		{"an image without a reference", `{"images": [{"name": "copy", "ref": ""}]}`},
		{"one name twice", `{"images": [{"name": "copy", "ref": "busybox:1"}, {"name": "copy", "ref": "busybox:2"}]}`},
		{"a digest of another form", `{"images": [{"name": "copy", "ref": "busybox:1", "digest": "md5:00"}]}`},
		{"an unknown field", `{"images": [{"name": "copy", "ref": "busybox:1", "credential": "x"}]}`},
		{"two documents", `{"images": [{"name": "copy", "ref": "busybox:1"}]} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			assert.Equal(t, http.StatusBadRequest, e.reportImages(t, e.withToken, tc.body))
			o := e.operatorOf(t)
			assert.Nil(t, o.ReportedAt)
			assert.Empty(t, o.Images)

			// Control: the same controller's valid report is stored.
			require.Equal(t, http.StatusNoContent,
				e.reportImages(t, e.withToken, `{"images": [{"name": "copy", "ref": "busybox:1"}]}`))
			assert.NotNil(t, e.operatorOf(t).ReportedAt)
		})
	}
}

func TestImages_ReportWithoutATokenIsRefusedAndStoresNothing(t *testing.T) {
	e := newEnv(t)
	assert.Equal(t, http.StatusUnauthorized,
		e.reportImages(t, e.withoutToken, `{"images": [{"name": "copy", "ref": "busybox:1"}]}`))
	operators, err := e.definitions.ListOperators(context.Background(), org)
	require.NoError(t, err)
	assert.Empty(t, operators)
}

func TestContact_IsWrittenAtMostOncePerMinutePerController(t *testing.T) {
	e := newEnv(t)
	first := e.clock.Now()
	require.Equal(t, http.StatusOK, e.desired(t, e.withToken, "").status)
	o := e.operatorOf(t)
	require.NotNil(t, o.LastContactAt)
	assert.True(t, first.Equal(*o.LastContactAt))

	// Within the minute, a request is served and its contact not written.
	e.clock.advance(59 * time.Second)
	require.Equal(t, http.StatusOK, e.desired(t, e.withToken, "").status)
	assert.True(t, first.Equal(*e.operatorOf(t).LastContactAt))

	// A minute after the last write, the next request writes its own time.
	e.clock.advance(time.Second)
	require.Equal(t, http.StatusOK, e.desired(t, e.withToken, "").status)
	assert.True(t, first.Add(time.Minute).Equal(*e.operatorOf(t).LastContactAt))
}

func TestContact_IsNotRecordedForARequestGaramDoesNotProve(t *testing.T) {
	e := newEnv(t)
	e.prover.setSession(verdict{err: distribution.ErrNotProved})
	assert.Equal(t, http.StatusForbidden, e.desired(t, e.withToken, "").status)
	operators, err := e.definitions.ListOperators(context.Background(), org)
	require.NoError(t, err)
	assert.Empty(t, operators)

	// Control: the same request, proved, records the contact.
	e.prover.setSession(verdict{proof: distribution.Proof{Operator: controller, Org: org}})
	require.Equal(t, http.StatusOK, e.desired(t, e.withToken, "").status)
	assert.NotNil(t, e.operatorOf(t).LastContactAt)
}
