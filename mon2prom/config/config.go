package config

import (
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/go-lager"
	sd "google.golang.org/api/monitoring/v3"
	"gopkg.in/yaml.v2"
)


//// Global Data Types ////

// Selector is the type used in each "For" entry, selecting which metrics to
// apply the associated settings to.  A missing or empty "For" entry matches
// every metric.
//
// For each top-level element in a Selector, specifying more than one value
// restricts to metrics matching _any_ of the values in that list ("or").
// For Not, that means that a metric is excluded if it matches any of the
// attributes listed ("nor").  But a metric only matches a Selector if it
// matches every non-empty, top-level element ("and").
//
// The letters used in Only and Not stand for: Cumulative, Delta, Gauge,
// Histogram, Float, Int, Bool, and String.
//
// For Unit, '' becomes '-' and values (or parts of values) like '{Bytes}'
// become '{}'.
type Selector struct {
	Prefix  []string    // Prefix(es) to match against full GCP metric paths.
	Suffix  []string    // Suffix(es) to match against Prom metric name.
	Only    string      // Required attributes (letters from "CDGHFIBS").
	Not     string      // Disallowed attributes (letters from "CDGHFIBS").
	Unit    string      // Required unit designation(s) (comma-separated).
}

// MetricMatcher contains the information about one type of GCP metric that
// is used when matching metrics against a Selector.  This is mostly just
// a GCP MetricDescriptor plus the last part of the metric name to be used
// in Prometheus (as computed so far).
type MetricMatcher struct {
	conf        Configuration
	MD          *sd.MetricDescriptor
	SubSys      string      // Middle part of Prom metric name.
	Name        string      // Last part of Prom metric name, so far.
	// Metric kind; one of 'C', 'D', or 'G' for cumulative, delta, or gauge.
	Kind        byte
	// Metric type; one of 'F', 'I', 'S', 'H', or 'B' for float, int, string,
	// histogram (distribution), or bool.
	Type        byte
	// MD.Unit but '' becomes '-' and values (or parts of values) like
	// '{Bytes}' are replaced by just '{}'.
	Unit        string
}

// The Configuration type specifies what data can be put in the gcp2prom.yaml
// configuration file to control which GCP metrics can be exported to
// Prometheus and to configure how each gets converted.
//
// Note that YAML lowercases the letters of keys so, for example, to set
// MaxBuckets, your YAML would contain something like `maxbuckets: 32`.
type Configuration struct {
	// System is the first part of each Prometheus metric name.
	System      string

	// Subsystem maps GCP metric path prefixes to the 2nd part of Prom metric
	// names.  Each key should be a prefix of some GCP metric names (paths) up
	// to (and including) a '/' character (for example,
	// "bigquery.googleapis.com/query/").  Metrics that do not match any of
	// these prefixes are silently ignored.
	//
	// The prefix is stripped from the GCP metric path to get the (first
	// iteration) of the last part of each metric name in Prometheus.  Any
	// '/' characters left after the prefix are left as-is until all Suffix
	// replacements are finished (see Suffix item below), at which point any
	// remaining '/' characters become '_' characters.
	//
	// Only the longest matching prefix is applied (per metric).
	Subsystem   map[string]string

	// Unit maps a unit name to the name of a predefined scaling factor to
	// convert to base units that are preferred in Prometheus.  The names
	// of the conversion functions look like multiplication or division
	// operations, often with repeated parts.  For example,
	// `"ns": "/1000/1000/1000"` to convert nanoseconds to seconds and
	// `"d": "*60*60*24"` to convert days to seconds.
	//
	// Each key is a string containing a comma-separated list of unit types.
	// If you use the same unit type in multiple entries, then which of those
	// entries that will be applied to a metric will be "random".
	Unit        map[string]string

	// Suffix is a list of rules for adjusting the last part of Prometheus
	// metric names by replacing a suffix.  Rules are applied in the order
	// listed.  The For element determines which rules apply to which metrics.
	//
	// For each rule, only the longest matching suffix is applied.  If a
	// metric matches the For element of multiple rules, then multiple rules
	// will be applied (in the order that they are listed) to that metric.
	//
	// A '/' character is prepended to the last part of the Prometheus name
	// before comparing it to each Replace key so you can use a key like
	// "/port_usage" to match (and replace) the whole name, not just a suffix.
	Suffix      []*struct {
		For     Selector            // Selects which metrics to rename.
		Replace map[string]string   // Key is suffix to be replaced with value.
		keys    []string            // Keys from Replace, longest to shortest.
	}

	// Histogram is a list of rules for resampling histogram metrics to reduce
	// the number of buckets or to simply ignore histogram metrics with too
	// many buckets.  If MinBound, MinRatio, and MaxBound are all 0.0 (or
	// missing), then they are ignored and only MaxBuckets applies.
	//
	// The rules are evaluated in the order listed and only the first
	// matching rule (for each metric) is applied.
	//
	// Note that if the Unit element (above) specifies a scaling factor for
	// this metric, then that scaling is applied to the bucket boundaries
	// (and not the MinBound nor MaxBound) before any resampling is done.
	Histogram   []struct {
		// The For element specifies which metrics match this rule.  If
		// missing (or empty), then the rule applies to all histogram metrics
		// (except those already matched by a prior rule) -- this only makes
		// sense for the last rule listed.
		For         Selector
		// MinBuckets specifies the minimum number of buckets needed for
		// resampling to happen.  If there are fewer than MinBuckets buckets,
		// then the buckets are not resampled and are preserved as-is.
		MinBuckets  int
		// MinBound specifies the minimum allowed bucket boundary.  If the
		// first bucket boundary is below this value, then the lowest
		// bucket boundary that is at least MinBound becomes the first
		// bucket boundary (and the counts for all prior buckets get
		// combined into the new first bucket).  Since metric values can
		// be negative, a MinBound of 0.0 is not ignored (unless MaxBound
		// is also 0.0).
		MinBound,
		// MinRatio specifies the minimum ratio between adjacent bucket
		// boundaries.  If the ratio between two adjacent bucket boundaries
		// (larger boundary divided by smaller boundary) is below MinRatio,
		// then the larger boundary (at least) will be removed, combining the
		// counts from at least two buckets into a new, wider bucket.
		//
		// If MinRatio is 0.0, then no minimum is applied.  Specifying a
		// MinRatio at or below 1.0 that is not 0.0 is a fatal error.
		MinRatio,
		// As bucket boundaries are iterated over from smallest to largest,
		// eliminating some due to either MinBound or MinRatio, if a bucket
		// boundary is reached that is larger than MaxBound, then the prior
		// kept bucket boundary becomes the last (largest) bucket boundary.
		MaxBound    float64
		// If the number of buckets (after resampling, if any was configured
		// in this rule) is larger than MaxBuckets, then the metric is just
		// ignored and will not be exported to Prometheus.
		MaxBuckets  int
	}

	// OmitLabels specifies rules for identifying labels to be omitted from
	// the metrics exported to Prometheus.  This is usually used to remove
	// labels that would cause high-cardinality metrics.
	//
	// If a metric matches more than one rule, then any labels mentioned in
	// any of the matching rules will be omitted.
	OmitLabel   []struct {
		For     Selector    // Selects which metrics to check.
		Labels  []string    // The list of metric labels to ignore.
	}
}

type ScalingFunc func(float64) float64


//// Global Variables ////

var ConfigFile = "gcp2prom.yaml"

// Map from config file path to loaded Configuration
var configs = make(map[string]*Configuration)

var Scale = map[string]ScalingFunc{
	"*1024*1024*1024":  multiply( 1024.0*1024.0*1024.0 ),
	"*1024*1024":       multiply( 1024.0*1024.0 ),
	"*60*60*24":        multiply( 60.0*60.0*24.0 ),
	"/100":             divide( 100.0 ),
	"/1000":            divide( 1000.0 ),
	"/1000/1000":       divide( 1000.0*1000.0 ),
	"/1000/1000/1000":  divide( 1000.0*1000.0*1000.0 ),
}


//// Functions ////


func multiply(m float64) ScalingFunc {
	return func(f float64) float64 { return f*m }
}

func divide(d float64) ScalingFunc {
	return func(f float64) float64 { return f/d }
}


type LongestFirst []string
func (p LongestFirst) Len() int             { return len(p) }
func (p LongestFirst) Less(i, j int) bool   { return len(p[i]) > len(p[j]) }
func (p LongestFirst) Swap(i, j int)        { p[i], p[j] = p[j], p[i] }

func longestFirst(strs []string) {
	sort.Sort(LongestFirst(strs))
}

func longestKeysFirst(m map[string]string) []string {
	strs := make([]string, len(m))
	o := 0
	for k, _ := range m {
		strs[o] = k
		o++
	}
	longestFirst(strs)
	return strs
}


func commaSeparated(list string, nilForSingle bool) []string {
	if ! strings.Contains(list, ",") {
		if nilForSingle {
			return nil
		} else if list = strings.TrimSpace(list); "" == list {
			return []string{}
		} else {
			return []string{list}
		}
	}

	items := strings.Split(list, ",")
	o := 0
	for _, v := range items {
		v = strings.TrimSpace(v)
		if "" != v {
			items[o] = v
			o++
		}
	}
	return items[:o]
}


func LoadConfig(path string) Configuration {
	if "" == path {
		path = ConfigFile
	}

	conf := configs[path]
	if nil != conf {
		return *conf
	}
	conf = new(Configuration)

	r, err := os.Open(path)
	if nil != err {
		lager.Exit().Map("Can't read", path, "Error", err)
	}
	y := yaml.NewDecoder(r)
	y.SetStrict(true)
	err = y.Decode(conf)
	if nil != err {
		lager.Exit().Map("Invalid yaml", err, "In", path)
	}
	lager.Debug().Map("Loaded config", conf)

	for _, suf := range conf.Suffix {
		suf.keys = longestKeysFirst(suf.Replace)
	}

	u := conf.Unit
	for k, v := range u {
		if items := commaSeparated(k, true); nil != items {
			delete(u, k)
			for _, key := range items {
				if _, ok := u[key]; ok {
					lager.Warn().Map(".units has duplicate unit spec", key,
						"including in", k)
				} else {
					u[key] = v
				}
			}
		}
	}
	lager.Debug().Map("Expanded units scaling", conf.Unit)

	configs[path] = conf
	return *conf
}


// Constructs a MetricMatcher for the given GCP MetricDescriptor.  If a second
// argument is passed, it should be a string holding the path to a YAML file
// containing the configuration to be used for exporting the metric to
// Prometheus.
//
// Returns `nil` if the metric is not one that can be exported to Prometheus
// based on the configuration chosen (because no Subsystem has been configured
// for it).
func MatchMetric(
	md *sd.MetricDescriptor,
	configFile ...string,
) *MetricMatcher {
	mm := new(MetricMatcher)
	if 1 < len(configFile) {
		panic("Passed more than one configFile to MatchMetric()")
	} else if 1 == len(configFile) {
		mm.conf = LoadConfig(configFile[0])
	} else {
		mm.conf = LoadConfig("")
	}
	mm.MD = md
	mm.Kind, mm.Type, mm.Unit = mon.MetricAbbrs(md)
	mm.SubSys, mm.Name = subSystem(md.Type, mm.conf.Subsystem)
	if "" == mm.SubSys {
		return nil
	}
	mm.computeName()
	return mm
}


// Returns the subsystem name and remaining suffix based on a map of
// prefixes to subsystem names.  Ensures that the returned suffix begins
// with a '/' character.  Returns ("","") if there is no matching prefix.
func subSystem(path string, subs map[string]string) (subsys, suff string) {
	parts := strings.Split(path, "/")
	e := len(parts)
	for 1 < e {
		e--
		prefix := strings.Join(parts[0:e], "/") + "/"
		if subsys = subs[prefix]; "" != subsys {
			suff = path[len(prefix):]
			if '/' != suff[0] {
				suff = "/" + suff
			}
			return
		}
	}
	return "", ""
}


var notAllowed = regexp.MustCompile("[^a-zA-Z0-9_]+")

// Iterates over Configuration.Suffix rules to successively replace suffixes
// of the metric name to get the final Prometheus metric name.
func (mm *MetricMatcher) computeName() {
	for _, s := range mm.conf.Suffix {
		if ! mm.matches(s.For) {
			continue
		}
		for _, k := range s.keys {
			if strings.HasSuffix(mm.Name, k) {
				mm.Name = mm.Name[0:len(mm.Name)-len(k)] + s.Replace[k]
				if '/' != mm.Name[0] {
					mm.Name = "/" + mm.Name
				}
				break
			}
		}
	}

	o := 0
	for '/' == mm.Name[o] {
		o++
	}
	mm.Name = notAllowed.ReplaceAllString(mm.Name[o:], "_")
	mm.SubSys = notAllowed.ReplaceAllString(mm.SubSys, "_")
}


// Returns the full metric name to use in Prometheus.
func (mm *MetricMatcher) PromName() string {
	return mm.conf.System + "_" + mm.SubSys + "_" + mm.Name
}


// Returns `nil` or a function that scales float64 values from the units
// used in StackDriver to the base units that are preferred in Prometheus.
func (mm *MetricMatcher) Scaler() ScalingFunc {
	key := mm.conf.Unit[mm.Unit]
	if "" == key {
		return nil
	}
	f, ok := Scale[key]
	if !ok {
		lager.Exit().Map("Unrecognized scale key", key, "For unit", mm.Unit)
	}
	return f
}


// Returns minBuckets, minBound, minRatio, maxBound, and maxBuckets to use for
// histogram resampling for the given metric.
func (mm *MetricMatcher) HistogramLimits() (
	minBuckets int, minBound, minRatio, maxBound float64, maxBuckets int,
) {
	for _, s := range mm.conf.Histogram {
		if ! mm.matches(s.For) {
			continue
		}
		return s.MinBuckets, s.MinBound, s.MinRatio, s.MaxBound, s.MaxBuckets
	}
	return
}


func (mm *MetricMatcher) matches(s Selector) bool {
	if 0 < len(s.Prefix) {
		match := false
		for _, p := range s.Prefix {
			if strings.HasPrefix(mm.MD.Type, p) {
				match = true
				break
			}
		}
		if ! match {
			return false
		}
	}

	if 0 < len(s.Suffix) {
		match := false
		for _, s := range s.Suffix {
			if strings.HasSuffix(mm.Name, s) {
				match = true
				break
			}
		}
		if ! match {
			return false
		}
	}

	if "" != s.Only && ! Contains(s.Only, mm.Kind, mm.Type) {
		return false
	}

	if "" != s.Not && Contains(s.Not, mm.Kind, mm.Type) {
		return false
	}

	if list := commaSeparated(s.Unit, false); 0 < len(list) {
		match := false
		for _, u := range list {
			if u == mm.Unit {
				match = true
				break
			}
		}
		if ! match {
			return false
		}
	}

	return true
}


// Returns `true` if either `k` or `t` is contained in the string `set`.
func Contains(set string, k, t byte) bool {
	any := string([]byte{k,t})
	return strings.ContainsAny(set, any)
}


// Returns the label names to be dropped when exporting the passed-in
// StackDriver metric to Prometheus.
func (mm *MetricMatcher) OmitLabels() []string {
	labels := make([]string, 0)
	for _, s := range mm.conf.OmitLabel {
		if mm.matches(s.For) {
			labels = append(labels, s.Labels...)
		}
	}
	return labels
}
