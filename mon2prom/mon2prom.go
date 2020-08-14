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
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/label"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/value"
	"github.com/TyeMcQueen/go-lager"
	sd      "google.golang.org/api/monitoring/v3"   // StackDriver
)


// A vector of Prometheus metrics exported from a StackDriver metric.
// Each metric in a vector has a different set of label values.
type PromVector struct {
	ProjectID       string              // GCP Project Name (ID)
	MonDesc         *sd.MetricDescriptor
	PromName        string              // Metric name in Prometheus
	PromDesc        *prom.Desc
	scaler          func(float64) float64
	MetricKind      byte                // 'D'elta, 'G'auge, or 'C'ounter
	ValueType       byte                // Histogram, Int, Float, or Bool
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
// MetricDescriptor.  Returns `nil` if the metric cannot be exported.
func basicPromVec(
	projectID   string,
	md          *sd.MetricDescriptor,
) *PromVector {
	pv := PromVector{}
	match := config.MatchMetric(md)
	if nil == match {
		return nil
	}
	pv.ProjectID = projectID
	pv.MonDesc = md
	pv.MetricKind = match.Kind
	pv.ValueType = match.Type
	pv.scaler = match.Scaler()
	if 'H' == pv.ValueType && 'G' == pv.MetricKind {
		lager.Warn().Map("Ignoring Histogram Gauge", md.Type)
		return nil
	}
	pv.PromName = config.Config.System + "_" + match.SubSys + "_" +
		config.Config.MetricName(pv.MonDesc.Type, pv.MetricKind, pv.ValueType)
	return &pv
}


// Finishes initializing the PromVector using recent values from the source
// time series.  Returns `false` if the metric cannot be exported.
func (pv *PromVector) addTimeSeriesDetails(
	tss []*sd.TimeSeries,
) bool {
	hasProjectID := false
	resourceKeys := make(map[string]bool)
	for _, ts := range tss {
		for k, _ := range ts.Resource.Labels {
			resourceKeys[k] = true
			if "project_id" == k {
				hasProjectID = true
			}
		}
	}
	lager.Debug().Map("For", pv.PromName,
		"Labels", pv.MonDesc.Labels, "Resource keys", resourceKeys)

	pv.Set.Init(pv.OmitLabels(), pv.MonDesc.Labels, resourceKeys)
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
		return pv.resampleHist(dv)
	} else if 'H' == pv.ValueType {
		lager.Fail().Map(
			"Histogram metric lacks DistributionValue", tss[0].Points[0])
	}
	return true
}


func (pv *PromVector) resampleHist(
	dv      *sd.Distribution,
) bool {
	if eb := dv.BucketOptions.ExponentialBuckets; nil != eb {
		minBound, minRatio, maxBound := config.Config.
			HistogramLimits(pv.MonDesc.Unit)
		lager.Debug().Map("minBound", minBound,
			"minRatio", minRatio, "maxBound", maxBound)
		pv.BucketBounds, pv.SubBuckets = ExpBuckets(
			eb,
			'I' == pv.ValueType,
			pv.scaler,
			minBound, minRatio, maxBound,
		)
		lager.Debug().Map("bounds", pv.BucketBounds,
			"subBuckets", pv.SubBuckets)
		if nil == pv.BucketBounds {
			lager.Fail().Map(
				"Histogram has too many buckets", eb,
				"For", pv.MonDesc.Type,
				"Units", pv.MonDesc.Unit,
			)
			return false
		}
	} else {
		lager.Exit().List("Haven't implemented non-exponential buckets")
	}
	return true
}


// Looks up the label names to be ignored based on the PromVector's
// MetricDescriptor.
func (pv *PromVector) OmitLabels() []string {
	return config.Config.OmitLabels(pv.MonDesc)
}


// Initializes the Prometheus histogram buckets based on a StackDriver
// Exponential Distribution and an optional configuration meant to reduce
// the number of buckets.
func ExpBuckets(
	exp         *sd.Exponential,
	isInt       bool,
	scale       config.ScalingFunc,
	minBound,
	minRatio,
	maxBound    float64,
) ([]float64, []int) {
	bound := exp.Scale
	if nil != scale {
		bound = scale(bound)
	}
	nextBound := minBound
	pow := exp.GrowthFactor
	count := 1+exp.NumFiniteBuckets
	bounds := make([]float64, count)
	subBuckets := make([]int, count)
	skip := false
	if 32 < count {
		if 0.0 == minRatio {
			return nil, nil
		}
		skip = true
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
		if !skip || nextBound <= bound && bound <= maxBound {
			bounds[o] = newBound
			subBuckets[o]++
			o++
			nextBound = bound * minRatio
		} else {
			subBuckets[o]++
		}
		bound *= pow
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
	pv := basicPromVec(projectID, md)
	if nil == pv {
		return nil
	}

	tss := make([]*sd.TimeSeries, 0, 32)
	for ts := range monClient.StreamLatestTimeSeries(
		nil, projectID, md, 5, "24h",
	) {
		tss = append(tss, ts)
	}
	if ! pv.addTimeSeriesDetails(tss) {
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
	if 'D' != pv.MetricKind {
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
		tDelta('D' == pv.MetricKind),
		mon.MetricKind(pv.MonDesc),
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
