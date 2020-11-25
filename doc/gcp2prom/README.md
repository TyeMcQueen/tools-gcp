# gcp2prom

The gcp2prom daemon exports GCP metrics (formerly "StackDriver Monitoring")
so that they can be scraped by Prometheus.  This is similar to
https://github.com/prometheus-community/stackdriver_exporter but with the
following improvements:

* Metric names are much better.
* Metric values are converted into base units as recommended for Prometheus
    (milliseconds converted to seconds, for example).
* Each metric is collected based on the schedule GCP has set for it.
* GCP Delta metrics are converted to Prometheus Counter metrics, making their
    values easier to work with and easier to interpret accurately.
* The number of buckets in a GCP Distribution metric can be significantly
    reduced for the Prometheus Histogram to prevent high-cardinality metrics.
* High-cardinality labels are dropped.

The metric naming may sound like a minor point but the improvements are
significant.  Problems with the names used by stackdriver_exporter:

* The names are overly long.
* The names usually contain several redundancies.
* The names often do not include the units.  The units used for each metric
    are recorded separately in GCP and so are usually not referenced in the
    metric name, making the units for stackdriver_exporter metrics not obvious.
* The names from GCP Delta metrics are often misleading.  Delta metrics in GCP
    are based on a sample period which is not recorded in the metric name.  So
    the Prometheus name will mean something like "number of X" when it really
    records "rate of X per PERIOD" where PERIOD can be different for each
    metric.

Consider the following GCP metric:

    loadbalancing.googleapis.com/https/backend_request_count

stackdriver_exporter creates a Prometheus metric named

    stackdriver_https_lb_rule_loadbalancing_googleapis_com_https_backend_request_count

while gcp2prom uses the name

    gcp_lb_https_backend_requests_total

The stackdriver_exporter metric is not accurately a backend_request_count and
if you treated it as such and, for example, summed it over 10m, you could get
quite inaccurate numbers.  Because, by default, what happens is that every
60s (for this metric) the delta of the count of requests is recorded in GCP.
Then, every 5 minutes, that is sampled by stackdriver_exporter.  Then, every
30s, Prometheus scrapes that.

So over 10 minutes you have 20 points that represent 10 copies each of 2
values that each is a count of connections for 2 particular minutes of the 10
minutes that passed.  80% of the data published by GCP is simply ignored.

Now, if you treat this "request count" more accurately as a "request rate per
minute" (contrary to its name), then you get more accurate results (but still
ignore 80% of the data, so a 1-minute spike in traffic you only have a 20%
chance of seeing).  But a gauge metric for "request rate" is a poor choice
for Prometheus.

gcp2prom instead creates a Counter metric of backend requests (hence the
name ending in "_total", as expected with Prometheus Counter metrics).  100%
of the data gets recorded (and you don't have to configure how often to
sample this metric, because GCP tells us what the schedule is and gcp2prom
just follows that schedule).

You can use rate() and increase() and similar Prometheus functions on this
metric and get 100% accurate answers.  Now, current implementations of
Prometheus mostly ignore the accurate timestamps that gcp2prom exposes for
these metrics so the Counter metric appears to not change every other time it
is sampled so if you use too short of an interval in your call to rate(), then
you do not get a smooth graph, of course.

Consider another GCP metric:

    loadbalancing.googleapis.com/https/total_latencies

which stackdriver_exporter exports as:

    stackdriver_https_lb_rule_loadbalancing_googleapis_com_https_total_latencies

while gcp2prom produces:

    gcp_lb_https_total_latency_seconds

There is no indication that the stackdriver_exporter metric is measured in
milliseconds.  While the gcp2prom is in the recommended base unit, seconds,
and says so in the name.

The stackdriver_exporter metric uses 67 buckets while the gcp2prom one only
uses 18.  gcp2prom also drops the client_country and proxy_continent labels
because the former makes for very high cardinality metrics and the latter,
when combined with the buckets, can lead to high cardinality.  For
non-histogram metrics, only the client_country label is dropped.
