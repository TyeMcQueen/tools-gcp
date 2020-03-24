package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"google.golang.org/api/monitoring/v3"
)


var Usage = pflag.BoolP("?", "?", false,
	"Show usage information.")
var Quiet = pflag.BoolP("quiet", "q", false,
	"Don't show empty metrics names while working.")
var AlsoEmpty = pflag.BoolP("empty", "e", false,
	"Show all metrics, not just non-empty ones.")
var Depth = pflag.IntP("depth", "d", 0,
	"Group metrics by the first 1, 2, or up-to 3 parts of the metric path.")


func Join(sep string, strs ...string) string { return strings.Join(strs, sep) }


func usage() {
	fmt.Println(Join("\n",
	"nmt-stats [-qe] [-d=...] [project-id]",
	"  By default, shows which StackDriver metrics are not empty.",
	"  Every option can be abbreviated to its first letter.",
	"  -?           Show this usage information.",
	"  --quiet      Don't show names of empty metrics while searching.",
	"               Implied if -e given.",
	"  --empty      Show all metrics, not just non-empty ones.",
	"  --depth=1-3  Only show groups of metrics.  -d1 just shows service/.",
	"               -d2 shows service/object/.  -d3 can show svc/obj/sub/.",
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
	eol     string,
) {
	if *AlsoEmpty {
		fmt.Printf("%c%c %s %s %d+%d%s\n", k, t, md.Type, u,
			mon.IngestDelay(md)/time.Second,
			mon.SamplePeriod(md)/time.Second, eol)
	} else if 0 < count {
		fmt.Printf("%4d %c%c %s %s %d+%d%s\n",
			count, k, t, md.Type, u,
			mon.IngestDelay(md)/time.Second,
			mon.SamplePeriod(md)/time.Second, eol)
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

	if !*AlsoEmpty {
		if 0 == count && prior == prefix {
			return count, prior
		}
		if !*Quiet {
			parts := strings.Split(md.Type, "/")
			fmt.Printf("... %s/%s\r",
				Join("/", parts[0:len(parts)-1]...), eol)
		}
		count = 0
	}

	if !*AlsoEmpty {
		for _ = range client.StreamLatestTimeSeries(nil, proj, md, 5, "8h") {
			count++
		}
	}

	if 0 < *Depth {
		if *AlsoEmpty || 0 < count {
			fmt.Printf("%s%s\n", prefix, eol)
		}
	} else {
		DescribeMetric(count, md, k, t, u, eol)
	}
	return count, prefix
}


func main() {
	pflag.Parse()
	if 1 < len(pflag.Args()) || *Usage {
		usage()
	}
	eol := " \x1b[K"
	if *AlsoEmpty || *Quiet {
		eol = ""
	}

	proj := conn.DefaultProjectId()
	if 0 < len(pflag.Args()) {
		proj = pflag.Arg(0)
	}

	client := mon.MustMonitoringClient(nil)
	count, prior := -1, ""
	for md := range client.StreamMetricDescs(nil, proj, "") {
		count, prior = ShowMetric(client, proj, md, count, prior, eol)
	}
	fmt.Print(eol)
}
