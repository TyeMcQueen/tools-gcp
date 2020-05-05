package mon2prom

import (
	"os"
	"strings"

	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/go-lager"
	"google.golang.org/api/monitoring/v3"
	"gopkg.in/yaml.v2"
)


type config struct {
	System      string
	Units       map[string]string
	Subsystem   map[string]string
	Suffix      struct {
		Any         map[string]string
		Histogram   map[string]string
		Counter     map[string]string
		Gauge       map[string]string
	}
	MaxBuckets  int
	Histogram   map[string]struct {
		MinBound,
		MinRatio,
		MaxBound    float64
	}
	BadLabels   []struct {
		Labels  []string
		Prefix  string
		Suffix  string
		Only    string
		Not     string
		Units   string
	}
}


type conf struct { config }
var Config conf

type ScalingFunc func(float64) float64

var Scale = map[string]ScalingFunc{
	"*1024*1024*1024":  multiply( 1024.0*1024.0*1024.0 ),
	"*1024*1024":       multiply( 1024.0*1024.0 ),
	"*60*60*24":        multiply( 60.0*60.0*24.0 ),
	"/100":             divide( 100.0 ),
	"/1000":            divide( 1000.0 ),
	"/1000/1000":       divide( 1000.0*1000.0 ),
	"/1000/1000/1000":  divide( 1000.0*1000.0*1000.0 ),
}
func multiply(m float64) ScalingFunc {
	return func(f float64) float64 { return f*m }
}
func divide(d float64) ScalingFunc {
	return func(f float64) float64 { return f/d }
}


func init() {
	r, err := os.Open("sdconfig.yaml")
	if nil != err {
		lager.Exit().Map("Can't read config.yaml", err)
	}
	y := yaml.NewDecoder(r)
	err = y.Decode(&Config.config)
	if nil != err {
		lager.Exit().Map("Invalid yaml in config.yaml", err)
	}
	lager.Debug().Map("Loaded config", Config.config)
	h := Config.config.Histogram
	for k, v := range h {
		if strings.Contains(k, ",") {
			delete(h, k)
			for _, key := range strings.Split(k, ",") {
				h[key] = v
			}
		}
	}
	lager.Debug().Map("Histogram limits", Config.config.Histogram)
}


// Returns `nil` or a function that scales float64 values from the units
// used in StackDriver to the base units that are preferred in Prometheus.
func (c conf) Scaler(unit string) ScalingFunc {
	key := c.Units[unit]
	if "" == key {
		return nil
	}
	f, ok := Scale[key]
	if !ok {
		lager.Exit().Map("Unrecognized scale key", key, "For unit type", unit)
	}
	return f
}


// Returns the configured subsystem name for this metric type.
func (c conf) Subsystem(metricType string) string {
	parts := strings.Split(metricType, "/")
	e := len(parts)
	for 1 < e {
		e--
		prefix := strings.Join(parts[0:e], "/") + "/"
		if subsys := c.config.Subsystem[prefix]; "" != subsys {
			return subsys
		}
	}
	return ""
}


// Returns minBound, minRatio, and maxBound to use for histogram values when
// using the passed-in units.  If nothing is configured then 0.0, 0.0, 0.0 is
// returned.  Note that minBound and maxBound should be in the scaled (base)
// units used in Prometheus, not the StackDriver units given by the passed-in
// string.
func (c conf) HistogramLimits(unit string) (
	minBound, minRatio, maxBound float64,
) {
	h := c.Histogram[unit]
	return h.MinBound, h.MinRatio, h.MaxBound
}


// Returns the full metric name to use in Prometheus for the metric from
// StackDriver having the passed-in path, kind, and value type.
func (c conf) MetricName(path string, metricKind, valueType byte) string {
	for k, v := range c.Suffix.Any {
		if strings.HasSuffix(path, k) {
			path = path[0:len(path)-len(k)] + v
		}
	}
	var h map[string]string
	if 'H' == valueType {
		h = c.Suffix.Histogram
	} else if 'C' == metricKind || 'D' == metricKind {
		h = c.Suffix.Counter
	} else if 'G' == metricKind {
		h = c.Suffix.Gauge
	} else {
		lager.Panic().Map(
			"ValueType is not H", []byte{valueType},
			"MetricKind is no C nor G", []byte{metricKind},
		)
	}
	for k, v := range h {
		if strings.HasSuffix(path, k) {
			path = path[0:len(path)-len(k)] + v
		}
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}


// Returns `true` if either `k` or `t` is contained in the string `set`.
func Contains(set string, k, t byte) bool {
	any := string([]byte{k,t})
	return strings.ContainsAny(set, any)
}


// Returns the label names to be dropped when exporting the passed-in
// StackDriver metric to Prometheus.
func (c conf) BadLabels(md *monitoring.MetricDescriptor) []string {
	labels := make([]string, 0)
	k, t, u := mon.MetricAbbrs(md)
	path := md.Type
	for _, spec := range c.config.BadLabels {
		if "" != spec.Prefix && !strings.HasPrefix(path, spec.Prefix) ||
		   "" != spec.Suffix && !strings.HasSuffix(path, spec.Suffix) ||
		   "" != spec.Only && !Contains(spec.Only, k, t) ||
		   "" != spec.Not && Contains(spec.Not, k, t) ||
		   "" != spec.Units && u != spec.Units {
			continue
		}
		labels = append(labels, spec.Labels...)
	}
	return labels
}
