/*

The `value` package contains the types for holding individual metric values
(either just a float64 or the numeric state for a histogram).  These values
have no information about labels.  (The label names are held in a LabelSet
and the list of values for an individual metric are encoded as a RuneList
which is only used as the key to a map[label.RuneList]value.Metric.)

*/
package value

import (
	"sync"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/label"
	"github.com/golang/protobuf/proto"
	prom "github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	sd "google.golang.org/api/monitoring/v3" // StackDriver
)

// A minimal object that can be collected as a prometheus metric.
type Writer struct {
	PDesc  *prom.Desc
	Metric dto.Metric
}

func (mw Writer) Desc() *prom.Desc { return mw.PDesc }

func (mw Writer) Write(out *dto.Metric) error {
	*out = mw.Metric
	return nil
}

// A value.Metric is the interface for a single metric value.
type Metric interface {
	// Export() constructs a dto.Metric from a value.Metric, the label names
	// in a label.Set, and the label values encoded in a label.RuneList.
	Export(
		metricKind mon.MetricKind,
		valueType mon.ValueType,
		ls *label.Set,
		rl label.RuneList,
		bounds []float64,
	) dto.Metric

	// Copy() returns a deep, read-only copy of a value.Metric.
	Copy(time.Duration) Metric

	// Float() returns the float64 value of the value.Metric (for histograms,
	// this is the sum of the observations).
	Float() float64

	// GcpEpoch() returns the Unix epoch seconds of the "end" timestamp of
	// the metric as reported by GCP.  This usually equals the end of the
	// most recent available sample period (except for Gauge metrics that
	// usually have metric values with startTime == endTime and sometimes
	// multiple such values in a single sample period).
	//
	GcpEpoch() int64

	// IsReadOnly() returns true if the value is a read-only copy.  It
	// returns false if the value can receive updates.
	IsReadOnly() bool

	// AsReadOnly() returns the invocant but type-cast to be read-only.
	AsReadOnly() Metric
}

// A value.RwMetric is the interface to a value.Metric that also supports
// receiving updates.
type RwMetric interface {
	Metric
	AddFloat(float64)
	SetEpoch(int64)
}

// A value.Simple just contains a float64 metric value and a timestamp.  It
// implmenets the value.Metric interface.  It is read-only (but can be
// converted into a value.RwSimple).
//
type Simple struct {
	gcpEpoch  int64 // The epoch seconds of end of the sample period.
	promEpoch int64 // Epoch reported to Prometheus (may be more recent).
	val       float64
}

// A value.RwSimple is a value.Simple that can receive updates.
type RwSimple struct{ Simple }

// A value.Histogram holds the current state of a histogram metric.  It
// implmenets the value.Metric interface.  It is read-only (but can be
// converted into a value.RwHistogram).
//
type Histogram struct {
	Simple
	SampleCount uint64
	BucketHits  []uint64
}

// A value.RwHistogram is a value.Histogram that can receive updates.
type RwHistogram struct{ Histogram }

// A trivial cache so converting the same timestamp repeatedly is efficient:
var epochLock sync.RWMutex
var prevStamp = ""
var prevEpoch = int64(0)

// Converts a timestamp string into Unix epoch seconds.
func StampEpoch(stamp string) int64 {
	if "" == stamp {
		lager.Warn().WithStack(0, 0).List("Empty epoch")
		return 0
	}
	epochLock.RLock()
	if prevStamp == stamp {
		defer epochLock.RUnlock()
		return prevEpoch
	}
	epochLock.RUnlock()
	epochLock.Lock()
	defer epochLock.Unlock()

	when, err := time.Parse(time.RFC3339, stamp)
	if nil != err {
		lager.Warn().Map("Invalid metric timestamp", stamp, "Error", err)
		return 0
	}
	prevStamp = stamp
	prevEpoch = when.Unix()
	return prevEpoch
}

func (_ *Simple) IsReadOnly() bool      { return true }
func (_ *Histogram) IsReadOnly() bool   { return true }
func (_ *RwSimple) IsReadOnly() bool    { return false }
func (_ *RwHistogram) IsReadOnly() bool { return false }

// The following 3 methods work on all 4 Metric types:
func (sv *Simple) GcpEpoch() int64  { return sv.gcpEpoch }
func (sv *Simple) Float() float64   { return sv.val }
func (sv *Simple) PromEpoch() int64 { return sv.promEpoch }

func (sv *RwSimple) AddFloat(f float64)    { sv.val += f }
func (hv *RwHistogram) AddFloat(f float64) { hv.val += f }
func (sv *RwSimple) SetEpoch(e int64)      { sv.gcpEpoch = e; sv.promEpoch = e }
func (hv *RwHistogram) SetEpoch(e int64)   { hv.gcpEpoch = e; hv.promEpoch = e }

// Each of the following 4 methods also work on the corresponding Rw* type:

func (sv *Simple) AsReadOnly() Metric    { return sv }
func (hv *Histogram) AsReadOnly() Metric { return hv }

func (sv *Simple) Copy(samplePeriod time.Duration) Metric {
	copy := *sv
	copy.promEpoch += int64(samplePeriod.Seconds())
	return &copy
}

func (hv *Histogram) Copy(samplePeriod time.Duration) Metric {
	copy := *hv
	copy.promEpoch += int64(samplePeriod.Seconds())
	return &copy
}

// Simple's Export() returns a Protobuf version of a simple metric.
func (sv *Simple) Export(
	metricKind mon.MetricKind,
	valueType mon.ValueType,
	ls *label.Set,
	rl label.RuneList,
	_ []float64,
) (m dto.Metric) {
	m.Label = ls.LabelPairs(rl)
	m.TimestampMs = proto.Int64(1000 * sv.PromEpoch())

	if mon.THist != valueType {
		if mon.KGauge == metricKind {
			m.Gauge = &dto.Gauge{Value: proto.Float64(sv.Float())}
		} else if mon.KCount == metricKind || mon.KDelta == metricKind {
			m.Counter = &dto.Counter{Value: proto.Float64(sv.Float())}
		} else {
			lager.Fail().Map("Expect C or G MetricKind not",
				[]byte{byte(metricKind), byte(valueType)})
		}
	}
	return m
}

// Histogram's Export() returns a Protobuf version of a histogram metric.
func (hv *Histogram) Export(
	metricKind mon.MetricKind,
	valueType mon.ValueType,
	ls *label.Set,
	rl label.RuneList,
	bounds []float64,
) dto.Metric {
	m := hv.Simple.Export(metricKind, valueType, ls, rl, nil)

	h := dto.Histogram{}
	m.Histogram = &h
	h.SampleCount = proto.Uint64(hv.SampleCount)
	h.SampleSum = proto.Float64(hv.Float())
	cum := uint64(0)
	h.Bucket = make([]*dto.Bucket, len(bounds))
	for i, b := range bounds {
		cum += hv.BucketHits[i]
		h.Bucket[i] = &dto.Bucket{
			CumulativeCount: proto.Uint64(cum),
			UpperBound:      proto.Float64(b),
		}
	}
	return m
}

// Convert() converts a GCP Distribution value into a Prometheus histogram
// value which is then added to the invoking value.RwHistogram.  The sum of
// the (new) observations is returned so the caller can scale it and then add
// it in as well.
//
func (hv *RwHistogram) Convert(
	subBuckets []int,
	dv *sd.Distribution,
) float64 {
	if nil == hv.BucketHits {
		hv.BucketHits = make([]uint64, 1+len(subBuckets))
	}
	hv.SampleCount += uint64(dv.Count)
	o := 0
	subs := subBuckets[0]
	for _, n := range dv.BucketCounts {
		if 0 == subs {
			o++
			if o < len(subBuckets) {
				subs = subBuckets[o]
			} else {
				subs = 0
			}
		}
		subs--
		hv.BucketHits[o] += uint64(n)
	}
	return dv.Mean * float64(dv.Count)
}

// Populate() takes a GCP metric value (*TimeSeries) and computes a
// label.RuneList from the label values (both metric and resource labels)
// and converts the numeric data to a value.Metric and then stores that
// in the metricMap using the RuneList as the key.
//
func Populate(
	metricMap map[label.RuneList]Metric, // Where to store the value.Metric
	metricKind mon.MetricKind,
	valueType mon.ValueType,
	scaler func(float64) float64, // Optional unit change function
	ls *label.Set, //                Tracks label values seen
	subBuckets []int, //             N GCP buckets per Prom bucket
	ts *sd.TimeSeries,
	pt *sd.Point, //                 Which point from above to add
) {
	rl := ls.RuneList(ts.Metric.Labels, ts.Resource.Labels)
	lager.Debug().Map("RuneList", rl, "Labels", ts.Metric.Labels,
		"Resource", ts.Resource.Labels)
	mv := metricMap[rl]
	epoch := StampEpoch(pt.Interval.EndTime)
	if nil != mv && mv.IsReadOnly() && epoch == mv.GcpEpoch() {
		// If not read-only, then epoch _should_ match since metric has
		// already received an update this cycle.
		return // Got no new metrics, leave old values in place.
	}

	var wv RwMetric
	var f float64
	v := pt.Value
	if mon.THist == valueType {
		// We add in all intermediate points for Histograms since they are
		// always being converted from a Delta-like value to a Counter-like
		// value so we want a sum of all deltas since we started:
		var hv *RwHistogram
		if nil == mv {
			hv = new(RwHistogram)
		} else if mv.IsReadOnly() {
			hv = &RwHistogram{*mv.(*Histogram)}
		} else {
			hv = mv.(*RwHistogram)
		}
		f = hv.Convert(subBuckets, v.DistributionValue)
		hv.SetEpoch(epoch)
		wv = hv
	} else {
		sv := new(RwSimple)
		if nil != mv {
			if epoch == mv.GcpEpoch() { // This was updated during this round:
				// We must have omitted labels so multiple points will combine:
				sv.AddFloat(mv.Float())
			} else {
				// We got points from a prior period not captured last time:
				if mon.KDelta == metricKind {
					// For Delta values, we want to sum all points
					sv.AddFloat(mv.Float())
				} else if epoch < mv.GcpEpoch() {
					// pt is from older period, so use mv instead:
					sv.AddFloat(mv.Float())
					v = nil // Ignore v from pt so only sv is included.
				}
				// Else mv is the old one so ignore it.
			}
		}
		if nil != v {
			switch valueType {
			case mon.TBool:
				if 0.0 == sv.Float() && nil != v.BoolValue && *v.BoolValue {
					f = 1.0
				}
			case mon.TFloat:
				f = *v.DoubleValue
			case mon.TInt:
				f = float64(*v.Int64Value)
			default:
				lager.Panic().Map("ValueType not in [HFIB]", valueType)
			}
			sv.SetEpoch(epoch)
		}
		wv = sv
	}
	if nil != v {
		if nil != scaler {
			f = scaler(f)
		}
		wv.AddFloat(f)
	}
	metricMap[rl] = wv
}
