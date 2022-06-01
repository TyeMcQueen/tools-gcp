package main

import (
	"bytes"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/display"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	"google.golang.org/api/monitoring/v3"
)

var Usage = pflag.BoolP("?", "?", false,
	"Show usage instructions.")
var Quiet = pflag.BoolP("quiet", "q", false,
	"Don't show info on start-up about metrics exported.")
var Runners = pflag.IntP("runners", "r", -1,
	"How many threads to run to handle metric updates.")
var AsJson = pflag.BoolP("json", "j", false,
	"Log metric descriptions at start-up as JSON.")
var WithDesc = pflag.BoolP("desc", "d", false,
	"Show each metric's text description.")
var WithBuckets = pflag.BoolP("buckets", "b", false,
	"Show bucket information about any histogram metrics.")
var OnlyUnits = pflag.StringP("unit", "u", "",
	"Only export metrics with matching units (comma-separated).")
var PickUnit map[string]bool
var OnlyTypes = pflag.StringP("only", "o", "",
	"Only export metrics using any of the listed types/kinds (from CDGHFIBS).")
var NotTypes = pflag.StringP("not", "n", "",
	"Exclude metrics using any of the listed types/kinds (from CDGHFIBS).")
var Prefix = pflag.StringP("metric", "m", "",
	"Only export metrics that match the listed prefix(es) (comma-separated).")
var Prefixes []string

func usage() {
	fmt.Println(display.Join("\n",
		"gcp2prom [-eqjdb] [-{muon}=...] [project-id]",
		"  Reads GCP metrics and exports them for Prometheus to scrape.",
		"  Every option can be abbreviated to its first letter.",
		"  -?           Show this usage information.",
		"  --runners    Number of runners to use to fetch metrics (-r0 to test).",
		"  --quiet      Don't show info on start-up about metrics exported.",
		"  --json       Log metric descriptions (on start-up) as JSON.",
		"  --desc       Show each metric's text description.",
		"  --buckets    Show bucket information about any histogram metrics.",
		"  --metric=PRE Only export metrics with these prefix(es), comma-separated.",
		"  --unit=U,... Only export metrics with matching units, comma-separated.",
		"  --{only|not}={CDGHFIBS}",
		"      Only export (or exclude) metrics using any of the following types:",
		"          Cumulative Delta Gauge Histogram Float Int Bool String",
		"  Prepend 'G2P_' to long option name to get environment variable that",
		"    can be used in place of the option (ie. G2P_METRIC=loadbal).",
		"  Format of descriptions of metrics to be exported:",
		"    Count KindType Path Unit Delay+Period",
		"    [Desc]",
		"    [GCPBuckets]",
		"    [-Label -Label ...]",
		"    Count KindType Prom [Scale]",
		"    [PromBuckets]",
		"  Where:",
		"    Count  Number of distinct label combinations.",
		"    Kind   MetricKind: D, C, or G (delta, cumulative, gauge).",
		"    Type   ValueType:  H, F, I, B, or S (hist, float, int, bool, str).",
		"    Unit   The units the metric is declared to be measured in.",
		"           '' becomes '-' and values like '{Bytes}' become '{}'.",
		"    Delay  Duration before a sample becomes available.",
		"    Period Duration of each sample period.",
		"    Path   The full path of the GCP metric type.",
		"    -Label A label name that will be dropped from the exported metric.",
		"    Prom   The metric name exported to Prometheus.",
		"    Scale  How the metric's values are multiplied/divided for Prometheus.",
	))
	os.Exit(1)
}

func EnvOpts() {
	if !*Quiet && "" != os.Getenv("G2P_QUIET") {
		*Quiet = true
	}
	if !*AsJson && "" != os.Getenv("G2P_JSON") {
		*AsJson = true
	}
	if !*WithDesc && "" != os.Getenv("G2P_DESC") {
		*WithDesc = true
	}
	if !*WithBuckets && "" != os.Getenv("G2P_BUCKETS") {
		*WithBuckets = true
	}
	if env := os.Getenv("G2P_UNIT"); "" == *OnlyUnits && "" != env {
		*OnlyUnits = env
	}
	if env := os.Getenv("G2P_ONLY"); "" == *OnlyTypes && "" != env {
		*OnlyTypes = env
	}
	if env := os.Getenv("G2P_NOT"); "" == *NotTypes && "" != env {
		*NotTypes = env
	}
	if env := os.Getenv("G2P_METRIC"); "" == *Prefix && "" != env {
		*Prefix = env
	}
	if env := os.Getenv("G2P_RUNNERS"); -1 == *Runners && "" != env {
		if r, err := strconv.Atoi(env); nil != err {
			lager.Exit().Map("Non-integer G2P_RUNNERS value", env, "Error", err)
		} else if r < 0 {
			lager.Exit().Map("Negative G2P_RUNNERS value", r)
		} else {
			*Runners = r
		}
	}
	if *Runners < 0 {
		*Runners = 4
	}
}

func displayMetric(prom *mon2prom.PromVector, client mon.Client) {
	k, t := prom.MetricKind, prom.ValueType
	u, scale, gcpCount, bucketType, gcpBuckets := prom.ForHumans()

	if *AsJson {
		if mon.THist == t {
			lager.Info().Map(
				"metricDescriptor", prom.MonDesc,
				"gcpCount", gcpCount,
				"gcpBucketType", bucketType,
				"gcpBuckets", gcpBuckets,
				"kind", string([]byte{byte(k)}),
				"type", string([]byte{byte(t)}),
				"unit", u,
				"skippedKeys", prom.Set.SkippedKeys,
				"prometheusName", prom.PromName,
				"scale", scale,
				"promCount", len(*prom.MetricMap),
				"promBuckets", prom.BucketBounds,
			)
		} else {
			lager.Info().Map(
				"metricDescriptor", prom.MonDesc,
				"gcpCount", gcpCount,
				"kind", string([]byte{byte(k)}),
				"type", string([]byte{byte(t)}),
				"unit", u,
				"skippedKeys", prom.Set.SkippedKeys,
				"prometheusName", prom.PromName,
				"scale", scale,
				"promCount", len(*prom.MetricMap),
			)
		}
		return
	}

	fmt.Printf("%4d %c%c %s %s %s+%s\n",
		gcpCount, k, t, prom.MonDesc.Type, u,
		display.DurationString(mon.IngestDelay(prom.MonDesc)),
		display.DurationString(mon.SamplePeriod(prom.MonDesc)),
	)
	if *WithBuckets && nil != gcpBuckets {
		fmt.Printf("    %s", bucketType)
		display.DumpJson("", gcpBuckets)
	}
	if *WithDesc {
		fmt.Printf("    %s\n", display.WrapText(prom.MonDesc.Description))
	}

	if 0 < len(prom.Set.SkippedKeys) {
		l := new(bytes.Buffer)
		for i, k := range prom.Set.SkippedKeys {
			if 0 < i {
				l.WriteByte(' ')
			}
			l.WriteByte('-')
			l.Write([]byte(k))
		}
		fmt.Printf("    %s\n", l.String())
	}

	if k == mon.KDelta {
		k = mon.KCount
	}
	if "" != scale {
		scale = " " + scale
	}
	fmt.Printf("%4d %c%c %s%s\n",
		len(*prom.MetricMap), k, t, prom.PromName, scale)
	if mon.THist == t && *WithBuckets {
		fmt.Printf("    %v\n", prom.BucketBounds)
	}
	fmt.Printf("\n")
}

func export(
	proj string,
	client mon.Client,
	md *monitoring.MetricDescriptor,
	ch chan<- mon2prom.UpdateRequest,
) bool {
	k, t, u := mon.MetricAbbrs(md)
	if "" != *OnlyUnits && !PickUnit[u] ||
		"" != *OnlyTypes && !mon.Contains(*OnlyTypes, k, t) ||
		"" != *NotTypes && mon.Contains(*NotTypes, k, t) {
		return false
	}
	if 0 < len(Prefixes) {
		matched := false
		for _, p := range Prefixes {
			if strings.HasPrefix(md.Type, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	prom := mon2prom.NewVec(proj, client, md, ch)
	if nil == prom {
		return false
	}
	if !*Quiet {
		displayMetric(prom, client)
	}
	return true
}

func main() {
	if "" != os.Getenv("PANIC_ON_INT") {
		go tutl.ShowStackOnInterrupt()
	}
	pflag.Parse()
	if 1 < len(pflag.Args()) || *Usage {
		usage()
	}
	EnvOpts()
	*OnlyTypes = strings.ToUpper(*OnlyTypes)
	*NotTypes = strings.ToUpper(*NotTypes)
	if "" != *Prefix {
		Prefixes = strings.Split(*Prefix, ",")
	}
	if "" != *OnlyUnits {
		PickUnit = make(map[string]bool)
		for _, u := range strings.Split(*OnlyUnits, ",") {
			PickUnit[u] = true
		}
	}

	proj := ""
	if 0 < len(pflag.Args()) {
		proj = pflag.Arg(0)
	} else if dflt, err := lager.GcpProjectID(nil); nil != err {
		lager.Exit().MMap("Could not determine GCP Project ID", "err", err)
	} else {
		proj = dflt
	}

	monClient := mon.MustMonitoringClient(nil)
	ch, runner := mon2prom.MetricFetcher(monClient)
	count := 0
	for _, pref := range config.MustLoadConfig("").GcpPrefixes() {
		for md := range monClient.StreamMetricDescs(nil, proj, pref) {
			if export(proj, monClient, md, ch) {
				count++
			}
		}
	}
	if 0 == count {
		lager.Exit().List("No metrics found to export.")
	}
	if *Runners < 1 {
		lager.Exit().List("Not exporting metrics due to --runners=0.")
	}
	for i := 0; i < *Runners; i++ {
		go runner()
	}

	_ = time.AfterFunc(5*time.Minute, func() {
		if !mon2prom.WasScraped {
			lager.Warn().MMap(
				"Prometheus still hasn't scraped our metrics after 5 minutes")
		}
	})
	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/ready", http.HandlerFunc(
		func(rw http.ResponseWriter, _ *http.Request) {
			rw.WriteHeader(200)
		},
	))
	lager.Fail().MMap("Can't listen", "err", http.ListenAndServe(":8080", nil))
}
