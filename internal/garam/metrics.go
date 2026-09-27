package garam

import (
	"crypto/x509"
	"errors"
	"net"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The runnables a refusal is counted against, which is the value of the
// runnable label on [refusalsTotal].
const (
	runnablePoller   = "poller"
	runnableRenewer  = "renewer"
	runnableReporter = "reporter"
	runnableEnroller = "enroller"
)

// refusalHandshake is the kind label of a refusal garam made at the handshake,
// by sending an alert rather than answering a status.
const refusalHandshake = "handshake"

var (
	// certificateNotAfter is the notAfter of the certificate this operator last
	// read to authenticate to garam with. It has no labels and is a vector only
	// so that it exports no series until a certificate has been read: a gauge
	// reading zero would say the certificate expired in 1970.
	certificateNotAfter = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "garam_operator_certificate_not_after_timestamp_seconds",
		Help: "The notAfter of the certificate this operator last read to authenticate to garam with, " +
			"in Unix seconds.",
	}, nil)

	// refusalsTotal counts what garam refused, by the runnable that met the
	// refusal and its kind.
	refusalsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "garam_operator_refusals_total",
		Help: "Refusals garam answered this operator, by the runnable that met one and its kind: " +
			"\"handshake\" for an alert at the handshake, or the HTTP status garam answered.",
	}, []string{"runnable", "kind"})
)

func init() {
	metrics.Registry.MustRegister(certificateNotAfter, refusalsTotal)
}

// recordCertificate sets [certificateNotAfter] from the certificate read.
func recordCertificate(leaf *x509.Certificate) {
	certificateNotAfter.WithLabelValues().Set(float64(leaf.NotAfter.Unix()))
}

// countRefusal counts err against runnable where garam refused the call, and
// counts nothing for a nil error or a failure garam did not make.
func countRefusal(runnable string, err error) {
	if kind, refused := refusalKind(err); refused {
		refusalsTotal.WithLabelValues(runnable, kind).Inc()
	}
}

// refusalKind names what garam refused err with, and reports whether it refused
// at all.
//
// A handshake refusal is garam's alert, which crypto/tls reports as a net.OpError
// whose Op is "remote error". A certificate this operator could not read, a
// listener it did not verify, and a listener it did not reach are failures on
// this side and are not garam's to have refused.
func refusalKind(err error) (string, bool) {
	var refused *refusal
	if errors.As(err, &refused) {
		return strconv.Itoa(refused.status), true
	}
	var alert *net.OpError
	if errors.As(err, &alert) && alert.Op == "remote error" {
		return refusalHandshake, true
	}
	return "", false
}
