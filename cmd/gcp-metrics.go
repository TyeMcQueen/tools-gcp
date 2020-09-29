package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/tools-gcp/display"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"google.golang.org/api/monitoring/v3"
)


var Usage = pflag.BoolP("?", "?", false,
	"Show usage information.")
var Quiet = pflag.BoolP("quiet", "q", false,
	"Don't show empty metrics names while working.")
var AlsoEmpty = pflag.BoolP("empty", "e", false,
	"Show all metrics, not just non-empty ones.")
var AsJson = pflag.BoolP("json", "j", false,
	"Dump the full JSON of each metric descriptor.")
var ShowValues = pflag.BoolP("values", "v", false,
	"Dump the full JSON of most recent time-series values.")
var WithCount = pflag.BoolP("count", "c", false,
	"Show the count of each metric despite -e (very slow w/o -m=...).")
var WithHelp = pflag.BoolP("help", "h", false,
	"Show each metric's text description.")
var WithBuckets = pflag.BoolP("buckets", "b", false,
	"Show bucket information about any histogram metrics.")
var OnlyUnits = pflag.StringP("unit", "u", "",
	"Only show metrics with matching units (comma-separated).")
var ShowUnit map[string]bool
var OnlyTypes = pflag.StringP("only", "o", "",
	"Only show metrics using any of the listed types (from CDGHFIBS).")
var NotTypes = pflag.StringP("not", "n", "",
	"Exclude metrics using any of the listed types (from CDGHFIBS).")
var Prefix = pflag.StringP("metric", "m", "",
	"Only show metrics that match the listed prefix(es) (comma-separated).")
var Depth = pflag.IntP("depth", "d", 0,
	"Group metrics by the first 1, 2, or up-to 3 parts of the metric path.")


func usage() {
	fmt.Println(display.Join("\n",
	"gcp-metrics [-qejvhbc] [-[mudon]=...] [project-id]",
	"  By default, shows which GCP metrics are not empty.",
	"  Every option can be abbreviated to its first letter.",
	"  -?           Show this usage information.",
	"  --quiet      Don't show names of empty metrics while searching.",
	"               Implied if -e, -j, or -v given.",
	"  --empty      Show all metrics, not just non-empty ones.  Ignores -b.",
	"  --json       Dump the full JSON of each metric descriptor.",
	"  --values     Dump the JSON for the most recent metric values.",
	"               Without -j, outputs nothing but above.  -jv outputs both.",
	"               Either -j or -v ignores -h and -b.",
	"  --help       Show each metric's text description.",
	"  --buckets    Show bucket information about any histogram metrics.",
	"  --count      With -e, shows metric counts (as w/o -e).  Slow w/o -m.",
	"  --metric=PRE Only show metrics with these prefix(es), comma-separated.",
	"  --unit=ms    Only show metrics with matching units, comma-separated.",
	"  --depth=1-3  Only show groups of metrics.  -d1 just shows service/.",
	"               -d2 shows service/object/.  -d3 can show svc/obj/sub/.",
	"               -d causes -j, -v, -h, and -b to be ignored.",
	"  --{only|not}=[CDGHFIBS]",
	"      Only show (or exclude) metrics using any of the following types:",
	"          Cumulative Delta Gauge Histogram Float Int Bool String",
	"  Output is usually: Count KindType Path Units Delay+Period",
	"    Count  Number of distinct label combinations (unless -e given).",
	"    Kind   MetricKind: D, C, or G (delta, cumulative, gauge).",
	"    Type   ValueType:  H, F, I, B, or S (hist, float, int, bool, str).",
	"    Path   The full path of the metric type.",
	"    Units  The units the metric is declared to be measured in.",
	"           '' becomes '-' and values like '{Bytes}' become '{}'.",
	"    Delay  Number of seconds before a sample becomes available.",
	"    Period Number of seconds in each sample period.",
	))
	os.Exit(1)
}


func MetricPrefix(mdPath string, depth int) string {
	parts := strings.Split(mdPath, "/")
	if depth < 1 {
		depth = 2
	}
	if len(parts) <= depth {
		depth = len(parts)-1
	}
	return strings.Join(parts[0:depth], "/") + "/"
}


func DescribeMetric(
	count   int,
	md      *monitoring.MetricDescriptor,
	k       byte,
	t       byte,
	u       string,
	bType   string,
	buckets interface{},
	eol     string,
) {
	if *AlsoEmpty && ! *WithCount {
		fmt.Printf("%c%c %s %s %s+%s%s\n", k, t, md.Type, u,
			display.DurationString(mon.IngestDelay(md)),
			display.DurationString(mon.SamplePeriod(md)), eol)
	} else if 0 < count || *AlsoEmpty {
		fmt.Printf("%4d %c%c %s %s %s+%s%s\n",
			count, k, t, md.Type, u,
			display.DurationString(mon.IngestDelay(md)),
			display.DurationString(mon.SamplePeriod(md)), eol)
	} else {
		return
	}
	if *WithBuckets && nil != buckets {
		fmt.Printf("    %s", bType)
		display.DumpJson("", buckets)
	}
	if *WithHelp {
		fmt.Printf("    %s\n", display.WrapText(md.Description))
	}
}


func ShowMetric(
	client  mon.Client,
	proj    string,
	md      *monitoring.MetricDescriptor,
	count   int,
	prior   string,
	eol     string,
) (int, string) {
	prefix := MetricPrefix(md.Type, *Depth)
	if 0 < *Depth && "" != prior && strings.HasPrefix(prefix, prior) {
		return count, prior
	}
	k, t, u := mon.MetricAbbrs(md)
	if "" != *OnlyUnits && ! ShowUnit[u] ||
	   "" != *OnlyTypes && ! mon.Contains(*OnlyTypes, k, t) ||
	   "" != *NotTypes && mon.Contains(*NotTypes, k, t) {
		return count, prior
	}

	if !*AlsoEmpty {
		if 0 == count && prior == prefix {
			return count, prior
		}
		if !*Quiet {
			parts := strings.Split(md.Type, "/")
			fmt.Printf("... %s/%s\r",
				display.Join("/", parts[0:len(parts)-1]...), eol)
		}
	}
	count = 0

	bucketType, buckets := "", interface{}(nil)
	if !*AlsoEmpty || *WithCount || *ShowValues {
		for ts := range client.StreamLatestTimeSeries(nil, proj, md, 5, "8h") {
			count++
			if *Depth < 1 {
				if *ShowValues {
					if 1 < len(ts.Points) {
						ts.Points = ts.Points[0:1]
					}
					display.DumpJson("", ts)
				} else if 1 == count && 'H' == t {
					bucketType, buckets = display.BucketInfo(ts.Points[0].Value)
				}
			}
		}
	}

	if 0 < *Depth {
		if *AlsoEmpty || 0 < count {
			fmt.Printf("%s%s\n", prefix, eol)
		}
	} else if *AsJson {
		display.DumpJson("  ", md)
	} else if ! *ShowValues {
		DescribeMetric(count, md, k, t, u, bucketType, buckets, eol)
	}
	return count, prefix
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
	if "" != *OnlyUnits {
		ShowUnit = make(map[string]bool)
		for _, u := range strings.Split(*OnlyUnits, ",") {
			ShowUnit[u] = true
		}
	}
	eol := " \x1b[K"
	if *AlsoEmpty || *AsJson || *ShowValues || *Quiet {
		*Quiet = true
		eol = ""
	}

	proj := conn.DefaultProjectId()
	if 0 < len(pflag.Args()) {
		proj = pflag.Arg(0)
	}
	prefixes := strings.Split(*Prefix, ",")
	if 0 == len(prefixes) {
		prefixes = []string{""}
	}

	client := mon.MustMonitoringClient(nil)
	count, prior := -1, ""
	for _, search := range prefixes {
		for md := range client.StreamMetricDescs(nil, proj, search) {
			count, prior = ShowMetric(client, proj, md, count, prior, eol)
		}
	}
	fmt.Print(eol)
}
