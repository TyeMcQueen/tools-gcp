package mon2prom

// In this file we handle metrics covering how well gcp2prom is functioning.

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/mon"
)


var promCount = mon.NewGaugeVec(
	"gcp2prom", "metric", "values_total",
	"How many unique label sets are being exported to Prometheus.",
	"project_id", "metric", "delta", "kind",
)

var buckets = []float64{
	0.005, 0.01, 0.02, 0.04, 0.08, 0.15, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60,
}

var queueDelay = mon.NewHistVec(
	"gcp2prom", "metric", "update_queued_seconds",
	"How long it took for a queued metric update request to be received." +
	"  If high, more runners are needed for the number of metrics.",
	buckets,
	"project_id",
)


func init() {
	prometheus.MustRegister(promCount)
	prometheus.MustRegister(queueDelay)
}


func bLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}


func (pv *PromVector) promCountAdd() {
	countDiff := len(*pv.MetricMap) - len(pv.ReadOnlyMap())
	projectID := pv.ProjectID
	metric := pv.PromName
	isDelta := bLabel(mon.KDelta == pv.MetricKind)
	kind := mon.MetricKindLabel(pv.MonDesc)
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := promCount.GetMetricWithLabelValues(
			projectID, metric, isDelta, kind,
		)
		if nil != err {
			lager.Fail().Map("Can't get promtCount metric for labels", err)
			return
		}
		m.Add(float64(countDiff))
	}()
}


func (pv *PromVector) noteQueueDelay(queued, now time.Time) {
	projectID := pv.ProjectID
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := queueDelay.GetMetricWithLabelValues(projectID)
		if nil != err {
			lager.Fail().Map("Can't get queueDelay metric for labels", err)
			return
		}
		m.Observe(float64(now.Sub(queued)) / float64(time.Second))
	}()
}
