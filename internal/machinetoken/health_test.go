package machinetoken_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// longLived answers exchange n with a token living ten hours, which outlives the bound, so a
// test can hold one token across it.
func longLived(n int) (int, http.Header, string) {
	return http.StatusOK, nil, fmt.Sprintf(`{"access_token":"garam_ot_%064d","token_type":"Bearer","expires_in":36000}`, n)
}

// call sends one request through transport and answers the status it was answered, or zero where
// no answer came.
func call(t *testing.T, transport *machinetoken.Transport, method, url string, body io.Reader) int {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	if err != nil {
		return 0
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// failFor asks source for a token at every point its backoff allows across d, so the run of
// failures is as dense as it is in a source something calls through.
func failFor(source *machinetoken.Source, c *clock, d time.Duration) {
	for end := c.Now().Add(d); c.Now().Before(end); c.Add(time.Minute) {
		_, _ = source.Token(context.Background())
	}
}

func TestSource_CheckRaisesOnceEveryExchangeHasFailedForLongerThanTheBound(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusBadRequest, nil, `{"error":"invalid_client"}`
	}
	start := time.Unix(1_800_000_000, 0)
	c := &clock{now: start}
	source := newSource(s, files, c, "")

	_, err := source.Token(context.Background())
	require.ErrorIs(t, err, machinetoken.ErrInvalidClient, "setup: the first exchange is refused")
	failFor(source, c, machinetoken.SustainedFailure)
	require.Greater(t, len(s.sent()), 10, "setup: the backoff tried again throughout the bound")

	// Control: a run exactly as long as the bound is not yet sustained.
	require.Equal(t, start.Add(machinetoken.SustainedFailure), c.Now(), "setup: the run is the bound's length")
	require.NoError(t, source.Check(), "a run no longer than the bound is not reported")

	c.Add(time.Second)
	err = source.Check()
	require.ErrorIs(t, err, machinetoken.ErrInvalidClient, "a run longer than the bound is reported, by its cause")
	assert.Contains(t, err.Error(), "since 2027-01-15T08:00:00Z", "it names when the run began")
	assert.NotContains(t, err.Error(), "garam_ot_", "it names no token")
	assert.NotContains(t, err.Error(), "PRIVATE KEY", "it names no key")
}

func TestSource_CheckClearsOnTheFirstExchangeThatAnswersAToken(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusServiceUnavailable, nil, unavailable
	}
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")

	failFor(source, c, machinetoken.SustainedFailure+time.Minute)
	require.Error(t, source.Check(), "setup: the loss is reported")

	s.answer = func(n int) (int, http.Header, string) {
		return http.StatusOK, nil, fmt.Sprintf(`{"access_token":"garam_ot_%064d","token_type":"Bearer","expires_in":3600}`, n)
	}
	c.Add(5 * time.Minute)
	_, err := source.Token(context.Background())
	require.NoError(t, err, "setup: the exchange recovers")

	assert.NoError(t, source.Check(), "a token answered clears the report at once")
}

func TestSource_CheckStaysQuietThroughATransientFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
	}{
		{name: "a single 503", status: http.StatusServiceUnavailable},
		{name: "one throttled exchange", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"30"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, public := identity(t, t.TempDir())
			s := newStub(t, public)
			failing := true
			s.answer = func(n int) (int, http.Header, string) {
				if failing {
					return tc.status, tc.header, unavailable
				}
				return http.StatusOK, nil, fmt.Sprintf(`{"access_token":"garam_ot_%064d","token_type":"Bearer","expires_in":3600}`, n)
			}
			c := &clock{now: time.Unix(1_800_000_000, 0)}
			source := newSource(s, files, c, "")

			_, err := source.Token(context.Background())
			require.Error(t, err, "setup: the first exchange fails")
			failing = false
			c.Add(time.Minute)
			_, err = source.Token(context.Background())
			require.NoError(t, err, "setup: the next exchange answers a token")

			c.Add(machinetoken.SustainedFailure + time.Minute)
			assert.NoError(t, source.Check(), "a failure an exchange recovered from is never reported")

			// Control: the same failure, never recovered from, is reported over the same span.
			failing = true
			failFor(source, c, machinetoken.SustainedFailure+time.Minute)
			assert.Error(t, source.Check())
		})
	}
}

func TestTransport_ACallUnderTheHeldTokenEndsARunOfFailedRefreshes(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = longLived
	held := fmt.Sprintf("garam_ot_%064d", 1)
	s.accepted[held] = true
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")
	transport := &machinetoken.Transport{Source: source, Base: s.server.Client().Transport}

	require.Equal(t, http.StatusOK, call(t, transport, http.MethodGet, s.server.URL+"/protected", nil))
	s.answer = func(int) (int, http.Header, string) {
		return http.StatusServiceUnavailable, nil, unavailable
	}
	c.Add(7 * time.Hour)
	failFor(source, c, machinetoken.SustainedFailure+time.Minute)
	require.Error(t, source.Check(), "setup: every refresh has failed for longer than the bound")

	assert.Equal(t, http.StatusServiceUnavailable, call(t, transport, http.MethodGet, s.server.URL+"/unavailable", nil))
	assert.Error(t, source.Check(), "a 5xx says nothing of the token and ends nothing")

	assert.Equal(t, http.StatusOK, call(t, transport, http.MethodGet, s.server.URL+"/protected", nil))
	assert.NoError(t, source.Check(), "a call the held token was accepted on ends the run")
}

func TestTransport_CheckRaisesOnCallsRefused401ForLongerThanTheBound(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	s.answer = longLived
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")
	transport := &machinetoken.Transport{Source: source, Base: s.server.Client().Transport}
	// A body with no GetBody is not sent again, so each refusal is answered without a re-exchange
	// that would end the run.
	refused := func() int {
		return call(t, transport, http.MethodPost, s.server.URL+"/protected", io.NopCloser(strings.NewReader("{}")))
	}

	for end := c.Now().Add(machinetoken.SustainedFailure); !c.Now().After(end); c.Add(time.Minute) {
		require.Equal(t, http.StatusUnauthorized, refused())
	}
	require.Len(t, s.sent(), 1, "setup: one token was exchanged, and every call presenting it refused")

	err := source.Check()
	require.Error(t, err, "calls refused 401 for longer than the bound are reported")
	assert.Contains(t, err.Error(), "401")

	// Control: the same token accepted ends the run.
	s.accepted[fmt.Sprintf("garam_ot_%064d", 1)] = true
	require.Equal(t, http.StatusOK, refused())
	assert.NoError(t, source.Check())
}

func TestSource_KeepExchangesWhileNothingCallsThroughTheSource(t *testing.T) {
	files, public := identity(t, t.TempDir())
	s := newStub(t, public)
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	source := newSource(s, files, c, "")

	ctx, cancel := context.WithCancel(context.Background())
	kept := make(chan struct{})
	go func() {
		source.Keep(ctx)
		close(kept)
	}()

	require.Eventually(t, func() bool { return len(s.sent()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"Keep exchanges with no call made")
	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-kept:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "Keep returns once its context ends")
}
