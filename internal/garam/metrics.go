package garam

import (
	"errors"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/garamsh/garam-agent-operator/internal/machinetoken"
)

// The runnables a refusal is counted against, which is the value of the
// runnable label on [refusalsTotal].
const (
	runnablePoller   = "poller"
	runnableReporter = "reporter"
)

var (
	// exchangeFailuresTotal counts the exchanges for a token that failed, by the
	// token's audience and why. An operator whose exchanges fail goes on with the
	// token it holds until that expires, so a rise here is the notice ahead of
	// every call failing.
	exchangeFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "garam_operator_token_exchange_failures_total",
		Help: "Client assertions this operator failed to exchange for a garam token, by the token's " +
			"audience (\"garam\" or \"control\") and the reason: \"key\", \"invalid_client\", \"throttled\", " +
			"\"refused\" or \"unavailable\".",
	}, []string{"audience", "reason"})

	// refusalsTotal counts what garam refused, by the runnable that met the
	// refusal and its kind.
	refusalsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "garam_operator_refusals_total",
		Help: "Refusals garam answered this operator, by the runnable that met one and its kind: " +
			"the HTTP status garam answered.",
	}, []string{"runnable", "kind"})
)

func init() {
	metrics.Registry.MustRegister(exchangeFailuresTotal, refusalsTotal)
}

// ExchangeFailed returns what a token source for audience tells of a failed
// exchange: it is counted, and logged once per failure. The error names no key
// and no token.
func ExchangeFailed(log logr.Logger, audience string) func(*machinetoken.Error) {
	return func(failed *machinetoken.Error) {
		exchangeFailuresTotal.WithLabelValues(audience, string(failed.Reason)).Inc()
		log.Error(failed, "Failed to exchange a client assertion for a garam token",
			"audience", audience, "reason", failed.Reason, "until", failed.Until)
	}
}

// countRefusal counts err against runnable where garam refused the call, and
// counts nothing for a nil error or a failure garam did not make.
func countRefusal(runnable string, err error) {
	var refused *refusal
	if errors.As(err, &refused) {
		refusalsTotal.WithLabelValues(runnable, strconv.Itoa(refused.status)).Inc()
	}
}
