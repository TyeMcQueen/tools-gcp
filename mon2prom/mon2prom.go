/*
The mon2prom package supports reading TimeSeries metric values from GCP
(Google Cloud Platform) and exporting them as Prometheus metrics.
*/
package mon2prom

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/tools-gcp/display"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/label"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/value"
	prom "github.com/prometheus/client_golang/prometheus"
	sd "google.golang.org/api/monitoring/v3" // "StackDriver"
)

var WasScraped = false
var _firstCollection sync.Once

// Descriptive data for a GCP metric that we may want to report at start-up.
// This structure can be freed after start-up is finished.
//
type ForHumans struct {
	Unit       string
	Scale      string
	MonCount   int
	BucketType string
	MonBuckets interface{}
}

// A vector of Prometheus metrics exported from a GCP metric.
// Each metric in a vector has a different set of label values.
//
type PromVector struct {
	ProjectID    string // GCP Project Name (ID)
	MonDesc      *sd.MetricDescriptor
	PromName     string // Metric name in Prometheus
	PromDesc     *prom.Desc
	MetricKind   mon.MetricKind // 'D'elta, 'G'auge, or 'C'ounter
	ValueType    mon.ValueType  // Histogram, Int, Float, or Bool
	details      *ForHumans
	scaler       func(float64) float64
	BucketOpts   *sd.BucketOptions
	BucketBounds []float64 // Boundaries between hist buckets
	SubBuckets   []int     // Count of SD buckets in each Prom one.
	label.Set              // To build hash keys from label values.
	PrevEnd      string    // Timestamp of prior sample period end.
	PrevWhen     time.Time // When fetched prior period (debug).
	NextWhen     time.Time // When we will fetch next period.
	UpdateStart  time.Time // Used for debugging timing quirks.
	MetricMap    *map[label.RuneList]value.Metric
	ReadOnly     atomic.Value // Read-only metric map to export.
}

// exported metric timestamp (if lag < 1m) v-----v (if 1m <= lag)
//     prior metric v           metric v   |     |
//  (----- period -----](----- period -----]-lag-|-rand-|
//             prior fetch ^              current fetch ^

// What gets sent to request a metric be updated.
type UpdateRequest struct {
	pv     *PromVector
	queued time.Time
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
//
func MetricFetcher(monClient mon.Client) (chan<- UpdateRequest, func()) {
	ch := make(chan UpdateRequest, 5)
	return ch, func() {
		start := time.Now()
		for ur := range ch {
			start = ur.pv.noteQueueEmptyDuration(start)
			ur.pv.UpdateStart = start
			ur.pv.noteQueueDelay(ur.queued, start)
			ur.pv.Update(monClient, ch)
			ur.pv.noteUpdateDuration(start)
			start = time.Now()
		}
	}
}

// Creates a PromVector and partially initializes it based just on a
// MetricDescriptor.  Returns `nil`s if the metric cannot be exported.
//
func basicPromVec(
	projectID string,
	md *sd.MetricDescriptor,
) (*PromVector, *config.MetricMatcher) {
	pv := PromVector{}
	pv.details = new(ForHumans)
	matcher := config.MustLoadConfig("").MatchMetric(md)
	if nil == matcher {
		return nil, nil
	}
	if mon.SamplePeriod(md) < time.Minute {
		// GCP metrics with undeclared or very short sample periods can't
		// be exported unless we invent a reasonable sample period to use.
		// We have not implemented that yet.
		lager.Warn().MMap("Ignoring metric lacking long enough samplePeriod",
			"metric", md.Type, "period", mon.SamplePeriod(md),
			"metricDescriptor", md)
		return nil, nil
	}
	pv.ProjectID = projectID
	pv.MonDesc = md
	pv.MetricKind = matcher.Kind
	pv.ValueType = matcher.Type
	pv.details.Unit = matcher.Unit
	pv.scaler, pv.details.Scale = matcher.Scaler()
	if mon.TString == pv.ValueType {
		return nil, nil // Prometheus does not support string metrics.
	}
	if mon.THist == pv.ValueType && mon.KGauge == pv.MetricKind {
		// Treat Histogram Gauge as Delta, converting to Histogram Counter
		pv.MetricKind = mon.KDelta
	}
	pv.PromName = matcher.PromName()
	return &pv, matcher
}

// Finishes initializing the PromVector using recent values from the source
// time series.  Returns `false` if the metric cannot be exported.
//
func (pv *PromVector) addTimeSeriesDetails(
	matcher *config.MetricMatcher,
	tss []*sd.TimeSeries,
) bool {
	hasProjectID := false
	resourceKeys := make(map[string]bool)
	pv.details.MonCount = len(tss)
	for _, ts := range tss {
		if mon.THist == pv.ValueType && nil == pv.BucketOpts &&
			0 < len(ts.Points) {
			val := ts.Points[0].Value
			pv.BucketOpts = val.DistributionValue.BucketOptions
			pv.details.BucketType, pv.details.MonBuckets =
				display.BucketInfo(val)
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
		return false
	}
	return true
}

func bucketOptionsEqual(old, new *sd.BucketOptions) bool {
	if nil == old && nil == new {
		return true
	} else if nil == old || nil == new {
		return false
	} else if opb := old.ExponentialBuckets; nil != opb {
		if npb := new.ExponentialBuckets; nil == npb {
			return false
		} else {
			return opb.NumFiniteBuckets == npb.NumFiniteBuckets &&
				opb.Scale == npb.Scale &&
				opb.GrowthFactor == npb.GrowthFactor
		}
	} else if olb := old.LinearBuckets; nil != olb {
		if nlb := new.LinearBuckets; nil == nlb {
			return false
		} else {
			return olb.NumFiniteBuckets == nlb.NumFiniteBuckets &&
				olb.Offset == nlb.Offset &&
				olb.Width == nlb.Width
		}
	} else if oeb := old.ExplicitBuckets; nil != oeb {
		if neb := new.ExplicitBuckets; nil == neb {
			return false
		} else {
			for i, b := range oeb.Bounds {
				if b != neb.Bounds[i] {
					return false
				}
			}
			return true
		}
	}
	lager.Panic().MMap("Impossibility in bucketOptionsEqual()",
		"old", old, "new", new)
	return false // Not reached
}

func parseBucketOptions(
	name string, bucketOptions *sd.BucketOptions, scaler func(float64) float64,
) (
	boundCount int64, firstBound float64, nextBound func(float64) float64,
) {
	if pb := bucketOptions.ExponentialBuckets; nil != pb {
		boundCount = 1 + pb.NumFiniteBuckets
		firstBound = pb.Scale
		nextBound = func(b float64) float64 { return b * pb.GrowthFactor }
	} else if lb := bucketOptions.LinearBuckets; nil != lb {
		boundCount = 1 + lb.NumFiniteBuckets
		firstBound = lb.Offset
		nextBound = func(b float64) float64 { return b + lb.Width }
	} else if eb := bucketOptions.ExplicitBuckets; nil != eb {
		boundCount = int64(len(eb.Bounds))
		firstBound = eb.Bounds[0]
		if nil != scaler {
			for i, b := range eb.Bounds {
				eb.Bounds[i] = scaler(b)
			}
		}
		i := 0
		nextBound = func(_ float64) float64 { i++; return eb.Bounds[i] }
	} else {
		lager.Fail().Map(
			"Buckets were not exponential, linear, nor explicit for",
			name, "BucketOptions", bucketOptions)
	}
	if nil != scaler {
		firstBound = scaler(firstBound)
	}
	return
}

func (pv *PromVector) resampleHist(
	matcher *config.MetricMatcher,
	dv *sd.Distribution,
) bool {
	minBuckets, minBound, minRatio, maxBound, maxBuckets :=
		matcher.HistogramLimits()
	lager.Debug().Map("minBuckets", minBuckets, "minBound", minBound,
		"minRatio", minRatio, "maxBound", maxBound, "maxBuckets", maxBuckets)

	boundCount, firstBound, nextBound := parseBucketOptions(
		pv.MonDesc.Type, dv.BucketOptions, pv.scaler)
	if nil == nextBound {
		return false
	}

	pv.BucketBounds, pv.SubBuckets = combineBucketBoundaries(
		boundCount, firstBound, nextBound,
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
//
func combineBucketBoundaries(
	boundCount int64,
	firstBound float64,
	nextBound func(float64) float64,
	minBuckets int,
	minBound,
	minRatio,
	maxBound float64,
) ([]float64, []int) {
	bound := firstBound
	minNextBound := minBound
	if minBound == maxBound {
		minNextBound = firstBound // Ignore minBound
	}
	bounds := make([]float64, boundCount)
	subBuckets := make([]int, boundCount)
	resample := int64(minBuckets) < boundCount
	if 0.0 == minRatio {
		minRatio = 1.0
	}
	o := 0
	for i, _ := range subBuckets {
		if 0 < i {
			bound = nextBound(bound)
		}
		if !resample || minNextBound <= bound &&
			(minBound == maxBound || bound <= maxBound) {
			bounds[o] = bound
			subBuckets[o]++
			o++
			minNextBound = bound * minRatio
		} else {
			subBuckets[o]++
		}
	}
	return bounds[:o], subBuckets[:o]
}

// Creates a new PromVector, initializes it from recent TimeSeries data,
// and schedules it to be updated as time goes on.  Returns `nil` if the
// configuration does not specify how this metric should be exported.
//
func NewVec(
	projectID string,
	monClient mon.Client,
	md *sd.MetricDescriptor,
	ch chan<- UpdateRequest,
) *PromVector {
	pv, matcher := basicPromVec(projectID, md)
	if nil == pv {
		return nil
	}

	tss := make([]*sd.TimeSeries, 0, 32)
	pv.PrevWhen = time.Now()
	for ts := range monClient.StreamLatestTimeSeries(
		nil, projectID, md, 5, "24h",
	) {
		tss = append(tss, ts)
	}
	if !pv.addTimeSeriesDetails(matcher, tss) {
		return nil
	}

	pv.Clear()
	last := ""
	for _, ts := range tss {
		pt := ts.Points[0]
		end := pt.Interval.EndTime
		if "" == last || last < end {
			last = end
		}
		pv.Populate(ts, pt)
	}
	lager.Trace().Map("Exporting", pv.PromName, "From metrics", len(tss),
		"To metrics", len(*pv.MetricMap))
	pv.Publish()
	pv.Schedule(ch, last, 0)
	prom.MustRegister(pv)
	return pv
}

// Returns information that humans might want to know about the metric being
// exported and then frees up that information to save space.  Calling it a
// second time just gives you zero values for all of the items.
//
func (pv *PromVector) ForHumans() (
	unit string,
	scale string,
	count int,
	bucketType string,
	buckets interface{},
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
	_firstCollection.Do(func() {
		WasScraped = true
		lager.Note().MMap("Prometheus scraped our metrics for the first time")
	})
	for runelist, m := range pv.ReadOnlyMap() {
		ch <- value.Writer{
			PDesc: pv.PromDesc,
			Metric: m.Export(
				pv.MetricKind, pv.ValueType, &pv.Set, runelist, pv.BucketBounds,
			),
		}
	}
}

// Atomically fetches the pointer to the read-only map of read-only metrics,
// usually so that they can be exported by Collect() while updates might
// be simultaneously applied to the not-read-only map.
//
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
//
func (pv *PromVector) Clear() {
	m := make(map[label.RuneList]value.Metric)
	pv.MetricMap = &m
	if mon.KDelta != pv.MetricKind {
		return
	}
	ro := pv.ReadOnlyMap()
	for k, v := range ro {
		if !v.IsReadOnly() {
			lager.Panic().Map("Non-readonly value in read-only metric map", v)
		}
		m[k] = v.Copy(mon.SamplePeriod(pv.MonDesc))
	}
}

// Converts all of the metrics in pv.MetricMap to be read-only metrics and
// then atomically replaces the pointer to the old read-only map with the
// pointer in pv.MetricMap.
//
func (pv *PromVector) Publish() {
	pv.promCountAdd()
	m := *pv.MetricMap
	for k, v := range m {
		if !v.IsReadOnly() {
			v = v.AsReadOnly()
			m[k] = v
		}
	}
	pv.ReadOnly.Store(pv.MetricMap)
}

// Inserts or updates a single metric value in the metric map based on the
// latest sample period of the GCP metric.
//
func (pv *PromVector) Populate(ts *sd.TimeSeries, pt *sd.Point) bool {
	if dv := pt.Value.DistributionValue; nil != dv {
		if !bucketOptionsEqual(pv.BucketOpts, dv.BucketOptions) {
			return false
		}
	} else if nil != pv.BucketOpts {
		return false
	}
	value.Populate(
		*pv.MetricMap,
		pv.MetricKind,
		pv.ValueType,
		pv.scaler,
		&pv.Set,
		pv.SubBuckets,
		ts,
		pt,
	)
	return true
}

// Iterates over all of the TimeSeries values for a single GCP metric and
// Populates() them into the metric map.
//
func (pv *PromVector) Update(monClient mon.Client, ch chan<- UpdateRequest) {
	pv.Clear()
	last := pv.PrevEnd
	prevEpoch := value.StampEpoch(last)
	var ts *sd.TimeSeries
	valsPerPeriod := make(map[string]int)
	lateValues := 0
	start := time.Now()
	for ts = range monClient.StreamLatestTimeSeries(
		nil, pv.ProjectID, pv.MonDesc, 2, "0",
	) {
		for _, pt := range ts.Points {
			end := pt.Interval.EndTime
			if "" == last || last < end {
				last = end
			}
			if end < pv.PrevEnd { // Older than last sample:
				break // Don't care about this or any older points.
			} else if end == pv.PrevEnd {
				// From most recent prior period:
				rl := pv.Set.RuneList(ts.Metric.Labels, ts.Resource.Labels)
				mv := (*pv.MetricMap)[rl]
				if nil == mv || mv.GcpEpoch() < prevEpoch {
					// Found a value for last sample not found last time:
					lateValues++
					prior := "nil"
					if nil != mv {
						prior = conn.TimeAsString(time.Unix(mv.GcpEpoch(), 0))
					}
					lager.Trace().MMap(
						"Found skipped metric from prior period",
						"metric", pv.PromName,
						"metric labels", ts.Metric.Labels,
						"resource labels", ts.Resource.Labels,
						"more recent found end", prior,
						"prev period end", pv.PrevEnd,
						"sampled at", conn.TimeAsString(pv.PrevWhen),
					)
				}
			} else {
				valsPerPeriod[end]++
				pv.Populate(ts, pt)
			}
		}
	}
	count := 0
	if "" == last { // Impossible
		lager.Fail().Map(
			"Updated metric w/ no end epic", pv.PromName,
			"PrevEnd", pv.PrevEnd,
			"Samples fetched", len(ts.Points),
		)
	} else {
		count = valsPerPeriod[last]
		delete(valsPerPeriod, last)
	}
	lager.Trace().Map("Updated", pv.PromName, "From metrics", count,
		"To metrics", len(*pv.MetricMap))
	pv.addLateValues(lateValues)
	pv.addLatePeriods(len(valsPerPeriod))
	pv.Publish()
	pv.Schedule(ch, last, 1)
	pv.PrevWhen = start
}

// Schedules when to request that the values for this metric next be updated.
// We compute when the next sample period should be available (plus a few
// random seconds to reduce "thundering herd") and schedule pv to be sent
// to the metric runner's channel at that time.
//
func (pv *PromVector) Schedule(
	ch chan<- UpdateRequest,
	end string,
	seq int, // 0 if this is first try; 1 if not first try
) {
	now := time.Now()
	empty := false
	sample := mon.SamplePeriod(pv.MonDesc)
	delay := mon.IngestDelay(pv.MonDesc)
	if end == pv.PrevEnd {
		empty = true
		if when, err := time.Parse(time.RFC3339, end); nil != err {
			end = ""
			lager.Fail().Map("Invalid period end", end, "Error", err)
		} else {
			end = conn.TimeAsString(when.Add(sample))
		}
	}
	if "" == end {
		empty = true
		end = conn.TimeAsString(now)
	}
	epoch := value.StampEpoch(end)
	random := time.Duration((7.0 + 8.0*rand.Float64()) * float64(time.Second))
	when := time.Unix(epoch, 0)
	lager.Debug().Map(
		"For", pv.PromName,
		"Scheduling update after", when,
		"Plus sample period", float64(sample)/float64(time.Second),
		"Plus ingestion delay", float64(delay)/float64(time.Second),
		"Plus random pause", float64(random)/float64(time.Second),
	)
	when = when.Add(sample + delay + random)
	if when.Before(now) {
		if 0 == sample { // Avoid dividing by 0!
			lager.Fail().MMap("Tried to schedule metric with 0 sample period",
				"metric", pv.MonDesc)
			return // Stop fetching this metric
		}
		// How many sample periods do we need to skip to not be in the past?
		nPeriods := 1 + int64(now.Sub(when)/sample)
		// Duration we need to add to 'when':
		delta := sample * time.Duration(nPeriods)
		next := when.Add(delta)         // New 'when' after write log below.
		epoch += int64(delta.Seconds()) // Epoch of pv.PrevEnd+delta.
		log := lager.Trace()
		if next.Before(now) {
			log = lager.Panic() // Our math is broken and code needs fixing.
		} else if 0 < seq {
			// The first time we fetch, we can find that the most recent
			// available data is not from the most recent sample period.
			// So log such only at Trace and do not add to "skipped" metric.
			log = lager.Warn()
			pv.moreFFPeriods(nPeriods)
		}
		log.Map(
			"Skipping sample period for", pv.PromName,
			"gcpPath", pv.MonDesc.Type,
			"Previous period end", pv.PrevEnd,
			"Sampled at", conn.TimeAsString(pv.PrevWhen),
			"Latest period end", end,
			"This update scheduled", conn.TimeAsString(pv.NextWhen),
			"This update began", conn.TimeAsString(pv.UpdateStart),
			"Original schedule", conn.TimeAsString(when),
			"Now", conn.TimeAsString(now),
			"New schedule", conn.TimeAsString(next),
			"Sample Period", display.DurationString(sample),
			"Sample Delay", display.DurationString(delay),
			"Periods skipped", nPeriods,
			"This period empty?", empty,
		)
		end = conn.TimeAsString(time.Unix(epoch, 0))
		when = next
	}
	pv.PrevEnd = end
	pv.NextWhen = when
	time.AfterFunc(
		when.Sub(now), // Convert scheduled time to delay duration.
		func() {
			then := pv.noteTimerDelay(when)
			ch <- UpdateRequest{pv: pv, queued: then}
		},
	)
}
