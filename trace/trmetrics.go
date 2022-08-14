package trace

// In this file we handle Prometheus metrics about creating trace spans in GCP.

import (
	"time"

	"github.com/TyeMcQueen/tools-gcp/metric"
	"github.com/prometheus/client_golang/prometheus"
)

var buckets = []float64{
	0.005, 0.01, 0.02, 0.04, 0.08, 0.15, 0.25, 0.5, 1, 2, 4, 8,
}

var spanCreateSeconds = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "gcpapi", Subsystem: "span", Name: "create_seconds",
		Help:    "Seconds it took to register a span in GCP",
		Buckets: buckets,
	},
	[]string{"result"},
)

var spansDropped = prometheus.NewCounter(
	prometheus.CounterOpts{
		Namespace: "gcpapi", Subsystem: "span", Name: "dropped_total",
		Help: "Number of spans that were not registered due to backlog",
	},
)

func init() {
	prometheus.MustRegister(spanCreateSeconds)
	metric.MustRegister(nil) // For metric.NewCapacityUsage()
}

func spanCreated(start time.Time, result string) {
	spanCreateSeconds.WithLabelValues(result).Observe(
		float64(time.Now().Sub(start)) / float64(time.Second),
	)
}

func spanDropped() {
	spansDropped.Add(1)
}
