package desired

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// refusalsTotal counts every 4xx the control service answered, by the route
	// and the status.
	refusalsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "garam_operator_control_refusals_total",
		Help: "Refusals the control service answered this operator, by route (\"desired\" or \"status\") " +
			"and HTTP status.",
	}, []string{"route", "status"})

	// feedRefused is 1 while the desired feed's last answer was a refusal and 0
	// otherwise, so that an alert can fire on a refusal that does not clear.
	feedRefused = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "garam_operator_control_feed_refused",
		Help: "1 while the control service's last answer to the desired feed was a 4xx, 0 otherwise.",
	})
)

func init() {
	metrics.Registry.MustRegister(refusalsTotal, feedRefused)
}
