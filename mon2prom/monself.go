package mon2prom

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/mon"
)


type tDelta bool
const isDelta = tDelta(true)


var promCount = mon.NewGaugeVec(
	"gcp", "to_prom", "values_total",
	"How many unique label sets being exported to Prometheus",
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


func promCountAdd(
	countDiff   int,
	projectID   string,
	metric      string,
	isDelta     tDelta,
	kind        string,
) {
	m, err := promCount.GetMetricWithLabelValues(
		projectID, metric, bLabel(bool(isDelta)), kind,
	)
	if nil != err {
		lager.Fail().Map("Can't get promtCount metric for labels", err)
		return
	}
	m.Add(float64(countDiff))
}
