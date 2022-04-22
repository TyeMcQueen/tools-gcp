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

var ffCount = mon.NewHistVec(
	"gcp2prom", "metric", "fast_forward_sample_periods",
	"Number of sample periods we had to skip when scheduling next fetch." +
	"  Either a bug in the gcp2prom code or processing is bogged down.",
	[]float64{1, 2, 4, 8, 16},
	"project_id", "metric", "gcp_path",
)

var lateValueCount = mon.NewCounterVec(
	"gcp2prom", "metric", "read_too_late_values",
	"How many metric values found later from a period we previously fetched." +
	"  May happen if local clock is 'fast' beyond our minimum 9s slack.",
	"project_id", "metric", "gcp_path",
)

var latePeriodCount = mon.NewCounterVec(
	"gcp2prom", "metric", "read_too_late_periods",
	"How many extra metric periods found between adjacent fetches." +
	"  May happen if processing becomes bogged down.",
	"project_id", "metric", "gcp_path",
)

var buckets = []float64{
	0.005, 0.01, 0.02, 0.04, 0.08, 0.15, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60,
}

var timerDelay = mon.NewHistVec(
	"gcp2prom", "metric", "update_timer_lag_seconds",
	"How late time.AfterFunc fired to queue a metric update request." +
	"  If high, perhaps local CPU is exhausted.",
	buckets,
	"project_id",
)

var queueDelay = mon.NewHistVec(
	"gcp2prom", "metric", "update_queued_seconds",
	"How long it took for a queued metric update request to be received." +
	"  If high, more runners are needed for the number of metrics.",
	buckets,
	"project_id",
)

var queueEmptyDuration = mon.NewHistVec(
	"gcp2prom", "metric", "update_queue_empty_seconds",
	"How long update thread waited for next update request.",
	buckets,
	"project_id",
)

var updateDuration = mon.NewHistVec(
	"gcp2prom", "metric", "update_processing_seconds",
	"How long it took to fetch and publish updated metric values." +
	"  Useful for figuring out why update threads may be busy.",
	buckets,
	"project_id", "metric", "delta", "kind",
)

func init() {
	prometheus.MustRegister(promCount)
	prometheus.MustRegister(ffCount)
	prometheus.MustRegister(lateValueCount)
	prometheus.MustRegister(latePeriodCount)
	prometheus.MustRegister(timerDelay)
	prometheus.MustRegister(queueDelay)
	prometheus.MustRegister(queueEmptyDuration)
	prometheus.MustRegister(updateDuration)
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

func (pv *PromVector) moreFFPeriods(nPeriods int64) {
	projectID := pv.ProjectID
	metric    := pv.PromName
	gcpPath   := pv.MonDesc.Type
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := ffCount.GetMetricWithLabelValues(
			projectID, metric, gcpPath,
		)
		if nil != err {
			lager.Fail().Map("Can't get ffCount metric for labels", err)
			return
		}
		m.Observe(float64(nPeriods))
	}()
}

func (pv *PromVector) addLateValues(lateValues int) {
	projectID := pv.ProjectID
	metric    := pv.PromName
	gcpPath   := pv.MonDesc.Type
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := lateValueCount.GetMetricWithLabelValues(
			projectID, metric, gcpPath,
		)
		if nil != err {
			lager.Fail().Map("Can't get lateValueCount metric for labels", err)
			return
		}
		m.Add(float64(lateValues))
	}()
}

func (pv *PromVector) addLatePeriods(latePeriods int) {
	projectID := pv.ProjectID
	metric    := pv.PromName
	gcpPath   := pv.MonDesc.Type
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := latePeriodCount.GetMetricWithLabelValues(
			projectID, metric, gcpPath,
		)
		if nil != err {
			lager.Fail().Map("Can't get lateValueCount metric for labels", err)
			return
		}
		m.Add(float64(latePeriods))
	}()
}

func (pv *PromVector) noteTimerDelay(when time.Time) time.Time {
	projectID := pv.ProjectID
	now := time.Now()
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := timerDelay.GetMetricWithLabelValues(projectID)
		if nil != err {
			lager.Fail().Map("Can't get timerDelay metric for labels", err)
			return
		}
		m.Observe(float64(now.Sub(when)) / float64(time.Second))
	}()
	return now
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

func (pv *PromVector) noteQueueEmptyDuration(start time.Time) time.Time {
	projectID := pv.ProjectID
	now := time.Now()
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := queueEmptyDuration.GetMetricWithLabelValues(projectID)
		if nil != err {
			lager.Fail().Map(
				"Can't get queueEmptyDuration metric for labels", err)
			return
		}
		m.Observe(float64(now.Sub(start)) / float64(time.Second))
	}()
	return now
}

func (pv *PromVector) noteUpdateDuration(start time.Time) time.Time {
	projectID := pv.ProjectID
	metric := pv.PromName
	isDelta := bLabel(mon.KDelta == pv.MetricKind)
	kind := mon.MetricKindLabel(pv.MonDesc)
	now := time.Now()
	pv = nil        // Only use `pv` above this line!
	go func() {     // Don't block caller on prometheus locks:
		m, err := updateDuration.GetMetricWithLabelValues(
			projectID, metric, isDelta, kind,
		)
		if nil != err {
			lager.Fail().Map("Can't get updateDuration metric for labels", err)
			return
		}
		m.Observe(float64(now.Sub(start)) / float64(time.Second))
	}()
	return now
}
