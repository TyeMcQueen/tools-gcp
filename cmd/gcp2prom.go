package main

import (
	"bytes"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/tools-gcp/display"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/api/monitoring/v3"
)


var Usage = pflag.BoolP("?", "?", false,
	"Show usage instructions.")
var Quiet = pflag.BoolP("quiet", "q", false,
	"Don't show info on start-up about metrics exported.")
var NoOp = pflag.BoolP("exit", "e", false,
	"Just display which metrics would be exported then exit.")
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
	"  --exit       Just display which metrics would be exported then exit.",
	"  --quiet      Don't show info on start-up about metrics exported.",
	"  --json       Log metric descriptions (on start-up) as JSON.",
	"  --desc       Show each metric's text description.",
	"  --buckets    Show bucket information about any histogram metrics.",
	"  --metric=PRE Only export metrics with these prefix(es), comma-separated.",
	"  --unit=U,... Only export metrics with matching units, comma-separated.",
	"  --{only|not}={CDGHFIBS}",
	"      Only export (or exclude) metrics using any of the following types:",
	"          Cumulative Delta Gauge Histogram Float Int Bool String",
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


func displayMetric(prom *mon2prom.PromVector, client mon.Client) {
	k, t := prom.MetricKind, prom.ValueType
	u, scale, gcpCount, bucketType, gcpBuckets := prom.ForHumans()

	if *AsJson {
		if mon.THist == t {
			lager.Info().Map(
				"metricDescriptor", prom.MonDesc,
				"gcpCount",         gcpCount,
				"gcpBucketType",    bucketType,
				"gcpBuckets",       gcpBuckets,
				"kind",             string([]byte{byte(k)}),
				"type",             string([]byte{byte(t)}),
				"unit",             u,
				"skippedKeys",      prom.Set.SkippedKeys,
				"prometheusName",   prom.PromName,
				"scale",            scale,
				"promCount",        len(*prom.MetricMap),
				"promBuckets",      prom.BucketBounds,
			)
		} else {
			lager.Info().Map(
				"metricDescriptor", prom.MonDesc,
				"gcpCount",         gcpCount,
				"kind",             string([]byte{byte(k)}),
				"type",             string([]byte{byte(t)}),
				"unit",             u,
				"skippedKeys",      prom.Set.SkippedKeys,
				"prometheusName",   prom.PromName,
				"scale",            scale,
				"promCount",        len(*prom.MetricMap),
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
	proj        string,
	client      mon.Client,
	md          *monitoring.MetricDescriptor,
	ch          chan<- mon2prom.UpdateRequest,
) bool {
	k, t, u := mon.MetricAbbrs(md)
	if "" != *OnlyUnits && ! PickUnit[u] ||
	   "" != *OnlyTypes && ! mon.Contains(*OnlyTypes, k, t) ||
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
		if ! matched {
			return false
		}
	}
	prom := mon2prom.NewVec(proj, client, md, ch)
	if nil == prom {
		return false
	}
	if ! *Quiet {
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

	proj := conn.DefaultProjectId()
	if 0 < len(pflag.Args()) {
		proj = pflag.Arg(0)
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
	if *NoOp {
		lager.Exit().List("Not exporting metrics due to --exit.")
	}
	go runner()

	http.Handle("/metrics", promhttp.Handler())
	lager.Fail().Map("Can't listen", http.ListenAndServe(":8080", nil))
}
