package mon2prom

// In this file we handle metrics covering how well gcp2prom is functioning.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/mon"
)


var promCount = mon.NewGaugeVec(
	"gcp2prom", "metric", "values_total",
	"How many unique label sets are being exported to Prometheus.",
	"project_id", "metric", "delta", "kind",
)

func init() {
	prometheus.MustRegister(promCount)
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
