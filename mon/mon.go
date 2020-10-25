package mon

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"os"
	"strings"
	"time"

	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/go-lager"
	"google.golang.org/api/monitoring/v3"
)


type Client struct {
	*monitoring.Service
}

type MetricKind byte
const (
	KCount  MetricKind = 'C'
	KDelta  MetricKind = 'D'
	KGauge  MetricKind = 'G'
)

type ValueType byte
const (
	THist   ValueType = 'H'
	TFloat  ValueType = 'F'
	TInt    ValueType = 'I'
	TBool   ValueType = 'B'
	TString ValueType = 'S'
)

const QuotaExceeded = 429


func AsDuration(str string) time.Duration {
	dur, err := time.ParseDuration(str)
	if nil != err {
		lager.Exit().Map("Invalid duration", str, "Error", err)
	}
	return dur
}


// Returns `true` if either `k` or `t` is contained in the string `set`.
func Contains(set string, k MetricKind, t ValueType) bool {
	any := string([]byte{byte(k),byte(t)})
	return strings.ContainsAny(set, any)
}


func Timeout(duration string) context.Context {
	if "" == duration {
		duration = os.Getenv("MAX_QUERY_DURATION")
	}
	if "" == duration {
		duration = "10m"
	}
	ctx, _ := context.WithDeadline(
		context.Background(),
		time.Now().Add(AsDuration(duration)),
	)
	return ctx
}


func MustMonitoringClient(gcpClient *http.Client) Client {
	if nil == gcpClient {
		gcpClient = conn.MustGoogleClient()
	}
	monClient, err := monitoring.New(gcpClient)
	if err != nil {
		lager.Exit().Map("Can't connect to GCP metrics", err)
	}
	return Client{monClient}
}


var braces = regexp.MustCompile(`[{][^{}]+[}]`) // Non-greedy match for {.*}.

func MetricAbbrs(
	md *monitoring.MetricDescriptor,
) (MetricKind, ValueType, string) {
	k := MetricKind(md.MetricKind[0])
	t := ValueType(md.ValueType[0])
	if 'M' == k {
		k = 'K'     // 'K' for unspecified Kind (should never happen)
	}
	if 'V' == t {
		t = 'T'     // 'T' for unspecified Type (should never happen)
	}
	if 'D' == t {
		switch md.ValueType[1] {
			case 'O': t = TFloat    // Double -> Float
			case 'I': t = THist     // Distribution -> Histogram
		}
	}
	u := md.Unit
	if "" == u {
		u = "-"
	} else {
		u = braces.ReplaceAllString(u, "{}")
	}
	return k, t, u
}


func IngestDelay(md *monitoring.MetricDescriptor) time.Duration {
	if nil == md.Metadata || "" == md.Metadata.IngestDelay {
		return 10 * time.Second
	}
	return AsDuration(md.Metadata.IngestDelay)
}


func SamplePeriod(md *monitoring.MetricDescriptor) time.Duration {
	if nil == md.Metadata || "" == md.Metadata.SamplePeriod {
		return 0 * time.Second
	}
	return AsDuration(md.Metadata.SamplePeriod)
}


func (m Client) StreamLatestTimeSeries(
	ctx         context.Context,
	projectID   string,
	md          *monitoring.MetricDescriptor,
	maxPeriods  int,
	maxDuration string,
) (<-chan *monitoring.TimeSeries) {
	ch := make(chan *monitoring.TimeSeries, 1)
	go func() {
		m.GetLatestTimeSeries(ctx, ch, projectID, md, maxPeriods, maxDuration)
		close(ch)
	}()
	return ch
}


type tsLister = *monitoring.ProjectsTimeSeriesListCall

func (m Client) tsListLatest(
	projectID   string,
	delay,
	period      time.Duration,
	maxPeriods  int,
	maxDuration string,
) tsLister {
	if 0 == period {
		period = 2 * time.Minute
	}
	if maxPeriods < 1 {
		maxPeriods = 1
	}
	maxSpan := AsDuration(maxDuration)
	span := time.Duration(maxPeriods)*period
	if time.Duration(0) < maxSpan && maxSpan < span {
		maxPeriods = 1 + int(maxSpan/period)
		span = time.Duration(maxPeriods)*period
	}

	finish := time.Now().Add(-delay)
	start := finish.Add(-span)
	sStart  := start.In(time.UTC).Format(time.RFC3339)
	sFinish := finish.In(time.UTC).Format(time.RFC3339)
	lister :=
		m.Projects.TimeSeries.List(
			"projects/" + projectID,
		).IntervalStartTime(
			sStart,
		).IntervalEndTime(
			sFinish,
		)
	return lister
}


func MetricKindLabel(md *monitoring.MetricDescriptor) string {
	kind := "other"
	if "DISTRIBUTION" == md.ValueType {
		kind = "histogram"
	} else if "GAUGE" == md.MetricKind {
		kind = "gauge"
	} else {
		kind = "counter"
	}
	return kind
}


func (m Client) GetLatestTimeSeries(
	ctx         context.Context,
	ch          chan<- *monitoring.TimeSeries,
	projectID   string,
	md          *monitoring.MetricDescriptor,
	maxPeriods  int,
	maxDuration string,
) {
	if nil == ctx {
		ctx = Timeout("")
	}
	canceled := ctx.Done()
	if nil == md.Metadata {
		lager.Debug().Map("No metadata for metric", md.Type)
		return
	}
	lister := m.tsListLatest(
		projectID, IngestDelay(md), SamplePeriod(md), maxPeriods, maxDuration,
	).Filter(
		fmt.Sprintf(`metric.type="%s"`, md.Type),
	)
	delta := tDelta("DELTA" == md.MetricKind)
	kind := MetricKindLabel(md)
	first, last := isFirst, !isLast
	for ! last {
		start := time.Now()
		page, err := lister.Do()
		for nil != err && QuotaExceeded == conn.ErrorCode(err) {
			lager.Debug().Map("Quota exhaustion error", err)
			lager.Warn().List("Sleeping due to quota exhaustion")
			time.Sleep(20*time.Second)
			page, err = lister.Do()
		}
		if err != nil {
			if 400 != conn.ErrorCode(err) ||
			   !strings.Contains(err.Error(), "and monitored resource") {
				lager.Fail().Map("Error getting page of Time Series", err,
					"Code", conn.ErrorCode(err), "Metric", md.Type)
			}
			go tsPageSecs(start, projectID, delta, kind, first, isLast, err)
			return
		}
		last = tLast(nil == page || "" == page.NextPageToken)
		go tsPageSecs(start, projectID, delta, kind, first, last, nil)
		if nil != page {
			go tsCountAdd(len(page.TimeSeries), projectID, delta, kind)
			for _, timeSeries := range page.TimeSeries {
				select {
				case <- canceled:
					return
				case ch <- timeSeries:
				}
			}
		}
	}
}


func (m Client) StreamMetricDescs(
	ctx context.Context, projectID, prefix string,
) (<-chan *monitoring.MetricDescriptor) {
	ch := make(chan *monitoring.MetricDescriptor, 1)
	go func() {
		m.GetMetricDescs(ctx, ch, projectID, prefix)
		close(ch)
	}()
	return ch
}


func (m Client) GetMetricDescs(
	ctx         context.Context,
	ch          chan<- *monitoring.MetricDescriptor,
	projectID   string,
	prefix      string,
) {
	if nil == ctx {
		ctx = Timeout("")
	}
	canceled := ctx.Done()
	lister := m.Projects.MetricDescriptors.List("projects/" + projectID)
	if "" != prefix {
		lister = lister.Filter(
			fmt.Sprintf(`metric.type = starts_with("%s")`, prefix),
		)
	}
	first := isFirst
	last := !isLast
	for !last {
		start := time.Now()
		page, err := lister.Do()
		for nil != err && QuotaExceeded == conn.ErrorCode(err) {
			lager.Warn().List("Sleeping due to quota exhaustion")
			time.Sleep(20*time.Second)
			page, err = lister.Do()
		}
		if err != nil {
			go mdPageSecs(start, projectID, first, isLast, err)
			lager.Fail().Map("Error getting page of Metric Descs", err)
			return
		}
		last = tLast(nil == page || "" == page.NextPageToken)
		go mdPageSecs(start, projectID, first, last, nil)
		first = !isFirst
		if nil != page {
			for _, md := range page.MetricDescriptors {
				select {
				case <- canceled:
					return
				case ch <- md:
				}
			}
		}
	}
}
