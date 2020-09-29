/*
The mon2prom package supports reading TimeSeries metric values from GCP
(Google Cloud Platform) and exporting them as Prometheus metrics.
*/
package mon2prom

import (
	"math/rand"
	"sync/atomic"
	"time"

	prom    "github.com/prometheus/client_golang/prometheus"
	"github.com/TyeMcQueen/tools-gcp/display"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/label"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/value"
	"github.com/TyeMcQueen/go-lager"
	sd      "google.golang.org/api/monitoring/v3"   // StackDriver
)


// Descriptive data for a GCP metric that we may want to report at start-up.
// This structure can be freed after start-up is finished.
type ForHumans struct {
	Unit            string
	Scale           string
	MonCount        int
	BucketType      string
	MonBuckets      interface{}
}

// A vector of Prometheus metrics exported from a StackDriver metric.
// Each metric in a vector has a different set of label values.
type PromVector struct {
	ProjectID       string              // GCP Project Name (ID)
	MonDesc         *sd.MetricDescriptor
	PromName        string              // Metric name in Prometheus
	PromDesc        *prom.Desc
	MetricKind      mon.MetricKind      // 'D'elta, 'G'auge, or 'C'ounter
	ValueType       mon.ValueType       // Histogram, Int, Float, or Bool
	details         *ForHumans
	scaler          func(float64) float64
	BucketBounds    []float64           // Boundaries between hist buckets
	SubBuckets      []int               // Count of SD buckets in each Prom one.
	label.Set                           // To build hash keys from label values.
	MetricMap       *map[label.RuneList]value.Metric
	ReadOnly        atomic.Value        // Read-only map for exporting from.
}


// Returns a runner that updates PromVector values and a channel that each
// PromVector uses to request an update.  Invoke the runner function once
// all of the PromVectors have been initialized:
//
//      ch, runner := mon2prom.MetricFetcher(monClient)
//      for md := range monClient.StreamMetricDescs(nil, projectID, prefix) {
//          mon2prom.NewVec(projectID, monClient, md, ch)
//      }
//      go runner()
func MetricFetcher(monClient mon.Client) (chan<- *PromVector, func()) {
	ch := make(chan *PromVector, 5)
	return ch, func() {
		for pv := range ch {
			pv.Update(monClient, ch)
		}
	}
}


// Creates a PromVector and partially initializes it based just on a
// MetricDescriptor.  Returns `nil`s if the metric cannot be exported.
func basicPromVec(
	projectID   string,
	md          *sd.MetricDescriptor,
) (*PromVector, *config.MetricMatcher) {
	pv := PromVector{}
	pv.details = new(ForHumans)
	matcher := config.MustLoadConfig("").MatchMetric(md)
	if nil == matcher {
		return nil, nil
	}
	pv.ProjectID = projectID
	pv.MonDesc = md
	pv.MetricKind = matcher.Kind
	pv.ValueType = matcher.Type
	pv.details.Unit = matcher.Unit
	pv.scaler, pv.details.Scale = matcher.Scaler()
	if mon.THist == pv.ValueType && mon.KGauge == pv.MetricKind {
		lager.Warn().Map("Ignoring Histogram Gauge", md.Type)
		return nil, nil
	}
	pv.PromName = matcher.PromName()
	return &pv, matcher
}


// Finishes initializing the PromVector using recent values from the source
// time series.  Returns `false` if the metric cannot be exported.
func (pv *PromVector) addTimeSeriesDetails(
	matcher *config.MetricMatcher,
	tss     []*sd.TimeSeries,
) bool {
	hasProjectID := false
	resourceKeys := make(map[string]bool)
	pv.details.MonCount = len(tss)
	for _, ts := range tss {
		if mon.THist == pv.ValueType && nil == pv.details.MonBuckets &&
		   0 < len(ts.Points) {
			pv.details.BucketType, pv.details.MonBuckets = display.BucketInfo(
				ts.Points[0].Value)
		}
		for k, _ := range ts.Resource.Labels {
			resourceKeys[k] = true
			if "project_id" == k {
				hasProjectID = true
			}
		}
	}
	lager.Debug().Map("For", pv.PromName,
		"Labels", pv.MonDesc.Labels, "Resource keys", resourceKeys)

	pv.Set.Init(matcher.OmitLabels(), pv.MonDesc.Labels, resourceKeys)
	constLabels := prom.Labels{}
	if !hasProjectID {
		constLabels = prom.Labels{"project_id": pv.ProjectID}
	}
	pv.PromDesc = prom.NewDesc(
		pv.PromName, pv.MonDesc.Description, pv.KeptKeys(), constLabels,
	)

	if 0 == len(tss) {
		lager.Debug().Map("Got 0 time series metrics for", pv.PromName)
		return false
	} else if 0 == len(tss[0].Points) {
		lager.Exit().Map("First time series has 0 points for", pv.PromName)
	}

	if dv := tss[0].Points[0].Value.DistributionValue; nil != dv {
		return pv.resampleHist(matcher, dv)
	} else if mon.THist == pv.ValueType {
		lager.Fail().Map(
			"Histogram metric lacks DistributionValue", tss[0].Points[0])
	}
	return true
}


func (pv *PromVector) resampleHist(
	matcher *config.MetricMatcher,
	dv      *sd.Distribution,
) bool {
	minBuckets, minBound, minRatio, maxBound, maxBuckets :=
		matcher.HistogramLimits()
	lager.Debug().Map("minBuckets", minBuckets, "minBound", minBound,
		"minRatio", minRatio, "maxBound", maxBound, "maxBuckets", maxBuckets)

	var boundCount  int64
	var firstBound  float64
	var nextBound   func(float64) float64
	if pb := dv.BucketOptions.ExponentialBuckets; nil != pb {
		boundCount = 1 + pb.NumFiniteBuckets
		firstBound = pb.Scale
		nextBound = func(b float64) float64 { return b * pb.GrowthFactor }
	} else if lb := dv.BucketOptions.LinearBuckets; nil != lb {
		boundCount = 1 + lb.NumFiniteBuckets
		firstBound = lb.Offset
		nextBound = func(b float64) float64 { return b + lb.Width }
	} else if eb := dv.BucketOptions.ExplicitBuckets; nil != eb {
		boundCount = int64(len(eb.Bounds))
		firstBound = eb.Bounds[0]
		i := 0
		nextBound = func(_ float64) float64 { i++; return eb.Bounds[i] }
	} else {
		lager.Fail().Map(
			"Buckets were not exponential, linear, nor explicit for",
			pv.MonDesc.Name, "Sample value", dv)
		return false
	}

	if nil != pv.scaler {
		firstBound = pv.scaler(firstBound)
	}
	pv.BucketBounds, pv.SubBuckets = combineBucketBoundaries(
		boundCount, firstBound, nextBound,
		mon.TInt == pv.ValueType,
		minBuckets, minBound, minRatio, maxBound,
	)
	lager.Debug().Map("bounds", pv.BucketBounds,
		"subBuckets", pv.SubBuckets)

	if 0 != maxBuckets && maxBuckets < len(pv.BucketBounds) {
		lager.Fail().Map(
			"Histogram has too many buckets", len(pv.BucketBounds),
			"For", pv.MonDesc.Type, "Units", pv.MonDesc.Unit)
		return false
	}
	return true
}


// Initializes the Prometheus histogram buckets based on bucket boundaries
// from a GCP metric and an optional configuration meant to reduce the number
// of buckets.
func combineBucketBoundaries(
	boundCount  int64,
	firstBound  float64,
	nextBound   func(float64) float64,
	isInt       bool,
	minBuckets  int,
	minBound,
	minRatio,
	maxBound    float64,
) ([]float64, []int) {
	bound := firstBound
	minNextBound := minBound
	if minBound == maxBound {
		minNextBound = firstBound   // Ignore minBound
	}
	bounds := make([]float64, boundCount)
	subBuckets := make([]int, boundCount)
	resample := int64(minBuckets) < boundCount
	if 0.0 == minRatio {
		minRatio = 1.0
	}
	o := 0
	for _, _ = range subBuckets {
		newBound := bound
		if isInt {
			trunc := int64(bound)
			if bound <= float64(trunc) {
				trunc--
			}
			newBound = float64(trunc)
		}
		if !resample || minNextBound <= bound &&
		   ( minBound == maxBound || bound <= maxBound ) {
			bounds[o] = newBound
			subBuckets[o]++
			o++
			minNextBound = bound * minRatio
		} else {
			subBuckets[o]++
		}
		bound = nextBound(bound)
	}
	return bounds[:o], subBuckets[:o+1]
}


// Creates a new PromVector, initializes it from recent TimeSeries data,
// and schedules it to be updated as time goes on.  Returns `nil` if the
// configuration does not specify how this metric should be exported.
func NewVec(
	projectID   string,
	monClient   mon.Client,
	md          *sd.MetricDescriptor,
	ch          chan<- *PromVector,
) *PromVector {
	pv, matcher := basicPromVec(projectID, md)
	if nil == pv {
		return nil
	}

	tss := make([]*sd.TimeSeries, 0, 32)
	for ts := range monClient.StreamLatestTimeSeries(
		nil, projectID, md, 5, "24h",
	) {
		tss = append(tss, ts)
	}
	if ! pv.addTimeSeriesDetails(matcher, tss) {
		return nil
	}

	pv.Clear()
	last := ""
	for _, ts := range tss {
		end := ts.Points[0].Interval.EndTime
		if "" == last || last < end {
			last = end
		}
		pv.Populate(ts)
	}
	lager.Info().Map("Exporting", pv.PromName, "From metrics", len(tss),
		"To metrics", len(*pv.MetricMap))
	pv.Publish()
	pv.Schedule(ch, last)
	prom.MustRegister(pv)
	return pv
}


func (pv *PromVector) ForHumans() (
	unit        string,
	scale       string,
	count       int,
	bucketType  string,
	buckets     interface{},
) {
	if nil != pv.details {
		unit = pv.details.Unit
		scale = pv.details.Scale
		count = pv.details.MonCount
		bucketType = pv.details.BucketType
		buckets = pv.details.MonBuckets
		pv.details = nil
	}
	return
}


// Just returns the prometheus *Desc for the metric.
func (pv *PromVector) Describe(ch chan<- *prom.Desc) {
	ch <- pv.PromDesc
}


// Writes out (in protobuf format) each metric in the vector.
func (pv *PromVector) Collect(ch chan<- prom.Metric) {
	for runelist, m := range pv.ReadOnlyMap() {
		ch <- value.Writer{
			PDesc:  pv.PromDesc,
			Metric: m.Export(
				pv.MetricKind, pv.ValueType, &pv.Set, runelist, pv.BucketBounds,
			),
		}
	}
}


// Atomically fetches the pointer to the read-only map of read-only metrics,
// usually so that they can be exported by Collect() while updates might
// be simultaneously applied to the not-read-only map.
func (pv *PromVector) ReadOnlyMap() map[label.RuneList]value.Metric {
	val := pv.ReadOnly.Load()
	if nil == val {
		return nil
	}
	return *val.(*map[label.RuneList]value.Metric)
}


// Replaces pv.MetricMap with an empty map (not disturbing the map that
// pv.MetricMap used to point to, which is now the read-only map).  If
// pv refers to a Delta metric kind, then the read-only metrics from the
// read-only map get (deep) copied into the new map, so that these Counter
// metrics don't lose their accumulated value.
func (pv *PromVector) Clear() {
	m := make(map[label.RuneList]value.Metric)
	pv.MetricMap = &m
	if mon.KDelta != pv.MetricKind {
		return
	}
	ro := pv.ReadOnlyMap()
	for k, v := range ro {
		if ! v.IsReadOnly() {
			lager.Panic().Map("Non-readonly value in read-only metric map", v)
		}
		m[k] = v.Copy()
	}
}


// Converts all of the metrics in pv.MetricMap to be read-only metrics and
// then atomically replaces the pointer to the old read-only map with the
// pointer in pv.MetricMap.
func (pv *PromVector) Publish() {
	go promCountAdd(
		len(*pv.MetricMap) - len(pv.ReadOnlyMap()),
		pv.ProjectID,
		pv.PromName,
		tDelta(mon.KDelta == pv.MetricKind),
		mon.MetricKindLabel(pv.MonDesc),
	)
	m := *pv.MetricMap
	for k, v := range m {
		if ! v.IsReadOnly() {
			v = v.AsReadOnly()
			m[k] = v
		}
	}
	pv.ReadOnly.Store(pv.MetricMap)
}


// Inserts or updates a single metric value in the metric map based on the
// latest sample period of the StackDriver metric.
func (pv *PromVector) Populate(ts *sd.TimeSeries) {
	value.Populate(
		*pv.MetricMap,
		pv.MetricKind,
		pv.ValueType,
		pv.scaler,
		&pv.Set,
		pv.SubBuckets,
		ts,
	)
}


// Iterates over all of the TimeSeries values for a single StackDriver
// metric and Populates() them into the metric map.
func (pv *PromVector) Update(monClient mon.Client, ch chan<- *PromVector) {
	pv.Clear()
	count := 0
	last := ""
	var ts *sd.TimeSeries
	for ts = range monClient.StreamLatestTimeSeries(
		nil, pv.ProjectID, pv.MonDesc, 1, "0",
	) {
		end := ts.Points[0].Interval.EndTime
		if "" == last || last < end {
			last = end
		}
		pv.Populate(ts)
		count++
	}
	lager.Info().Map("Updated", pv.PromName, "From metrics", count,
		"To metrics", len(*pv.MetricMap))
	pv.Publish()
	pv.Schedule(ch, last)
}


// Schedules when to request that the values for this metric next be updated.
// We compute when the next sample period should be available (plus a few
// random seconds to reduce "thundering herd") and schedule pv to be sent
// to the metric runner's channel at that time.
func (pv *PromVector) Schedule(ch chan<- *PromVector, end string) {
	epoch  := value.StampEpoch(end)
	sample := mon.SamplePeriod(pv.MonDesc)
	delay  := mon.IngestDelay(pv.MonDesc)
	random := time.Duration(rand.Float64()*float64(5*time.Second))
	when   := time.Unix(epoch, 0)
	lager.Debug().Map(
		"For", pv.PromName,
		"Scheduling update after", when,
		"Plus sample period", float64(sample)/float64(time.Second),
		"Plus ingestion delay", float64(delay)/float64(time.Second),
		"Plus random pause", float64(random)/float64(time.Second),
	)
	when = when.Add(sample+delay+random)
	now := time.Now()
	for when.Before(now) {
		// TODO: Increment "skipping sample period" metric!!!
		lager.Warn().Map("Skipping sample period for", pv.PromName)
		when = when.Add(sample)
	}
	time.AfterFunc(
		when.Sub(now),  // Convert scheduled time to delay duration.
		func() {
			ch <- pv    // TODO: Add `when` to this to monitor delay
		},
	)
}
