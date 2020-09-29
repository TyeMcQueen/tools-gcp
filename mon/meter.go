package mon

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/conn"
)


type tFirst    bool
type tLast     bool
type tDelta    bool

const isFirst   = tFirst(true)
const isLast    = tLast(true)
const isDelta   = tDelta(true)

var buckets = []float64{
	0.005, 0.01, 0.02, 0.04, 0.08, 0.15, 0.25, 0.5, 1, 2, 4, 8, 15,
}
var mdPageSeconds = NewHistVec(prometheus.HistogramOpts{
	Namespace: "gcp",  Buckets: buckets,
	Subsystem: "metric",  Name: "desc_page_latency_seconds",
	Help: "Seconds it took to fetch one page of metric descriptors from GCP",
},  "project_id", "first_page", "last_page", "code")
var tsPageSeconds = NewHistVec(prometheus.HistogramOpts{
	Namespace: "gcp",  Buckets: buckets,
	Subsystem: "metric",  Name: "page_latency_seconds",
	Help: "Seconds it took to fetch one page of metric values from GCP",
},  "project_id", "delta", "kind", "first_page", "last_page", "code")
var tsCount = NewCounterVec(prometheus.CounterOpts{
	Namespace: "gcp",  Subsystem: "metric",  Name: "values_total",
	Help: "How many metric values (unique label sets) fetched from GCP",
},  "project_id", "delta", "kind")


func init() {
	prometheus.MustRegister(mdPageSeconds)
	prometheus.MustRegister(tsPageSeconds)
	prometheus.MustRegister(tsCount)
}


func NewHistVec(
	opts prometheus.HistogramOpts, label_keys ...string,
) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(opts, label_keys)
}


func NewCounterVec(
	opts prometheus.CounterOpts, label_keys ...string,
) *prometheus.CounterVec {
	return prometheus.NewCounterVec(opts, label_keys)
}


func SecondsSince(start time.Time) float64 {
	return float64(time.Now().Sub(start)) / float64(time.Second)
}


func bLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}


func mdPageSecs(
	start       time.Time,
	projectID   string,
	isFirstPage tFirst,
	isLastPage  tLast,
	pageErr     error,
) {
	m, err := mdPageSeconds.GetMetricWithLabelValues(
		projectID,
		bLabel(bool(isFirstPage)),
		bLabel(bool(isLastPage)),
		strconv.Itoa(conn.ErrorCode(pageErr)),
	)
	if nil != err {
		lager.Fail().Map("Can't get mdPageSecs metric for labels", err)
		return
	}
	m.Observe(SecondsSince(start))
}


func tsPageSecs(
	start       time.Time,
	projectID   string,
	isDelta     tDelta,
	kind        string,
	isFirstPage tFirst,
	isLastPage  tLast,
	pageErr     error,
) {
	m, err := tsPageSeconds.GetMetricWithLabelValues(
		projectID,
		bLabel(bool(isDelta)),
		kind,
		bLabel(bool(isFirstPage)),
		bLabel(bool(isLastPage)),
		strconv.Itoa(conn.ErrorCode(pageErr)),
	)
	if nil != err {
		lager.Fail().Map("Can't get tsPageSecs metric for labels", err)
		return
	}
	m.Observe(SecondsSince(start))
}


func tsCountAdd(
	count       int,
	projectID   string,
	isDelta     tDelta,
	kind        string,
) {
	m, err := tsCount.GetMetricWithLabelValues(
		projectID,
		bLabel(bool(isDelta)),
		kind,
	)
	if nil != err {
		lager.Fail().Map("Can't get tsCount metric for labels", err)
		return
	}
	m.Add(float64(count))
}
