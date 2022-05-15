package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-tutl"
	sd "google.golang.org/api/monitoring/v3"
)

var _ = io.EOF
var _ = os.Stdout

func TestMisc(t *testing.T) {
	var u = tutl.New(t)

	// Commas
	u.IsNot(nil, commaSeparated(",", true), `"," not single`)
	u.IsNot(nil, commaSeparated("", true), `"" is single`)
	u.IsNot(nil, commaSeparated("x", true), `"x" is single`)

	u.Is("[]", commaSeparated("", false), `"" -> empty`)
	u.Is("[]", commaSeparated(",", false), `"," -> empty`)
	u.Is("[]", commaSeparated(" ,, ", false), `" ,, " -> nil`)

	// Scaling
	u.Is(1024.0*1024.0*1024.0, Scale["*1024*1024*1024"](1.0), "2^8^3")
	u.Is(1024.0*1024.0, Scale["*1024*1024"](1.0), "2^8^2")
//  u.Is(1024.0, Scale["*1024"](1.0), "2^8")
	u.Is(60.0*60.0*24.0, Scale["*60*60*24"](1.0), "day")

	u.Is(1.0, Scale["/100"](100.0), "percent")
	u.Is(1.0, Scale["/1000"](1000.0), "milli")
	u.Is(1.0, Scale["/1000/1000"](1000.0*1000.0), "micro")
	u.Is(1.0, Scale["/1000/1000/1000"](1000.0*1000.0*1000.0), "nano")

	// longestFirst
	{
		list := []string{"b", "ccc", "aa"}
		longestFirst(list)
		u.Is("[ccc aa b]", list, "longestFirst")
	}

	// longestKeysFirst
	{
		m := map[string]string{
			"b":   "bee",
			"ccc": "cees",
			"aa":  "ays",
		}
		u.Is("[ccc aa b]", longestKeysFirst(m), "longestKeysFirst")
	}

	// subSystem
	subs := map[string]string{
		"loadbalancing.googleapis.com/https/":          "lb_https",
		"loadbalancing.googleapis.com/https/internal/": "ilb_https",
		"loadbalancing.googleapis.com/l3/external/":    "lb_l3",
		"loadbalancing.googleapis.com/l3/internal/":    "ilb_l3",
		"loadbalancing.googleapis.com/tcp_ssl_proxy/":  "tcp_ssl_proxy",
		"bigquery.googleapis.com/query/":               "bigquery_query",
		"bigquery.googleapis.com/slots/":               "bigquery_slot",
		"bigquery.googleapis.com/storage/":             "bigquery_store",
	}
	subsys, suff := subSystem("lowballing.gobbleapis.edu/bids/estimate", subs)
	u.Is("", subsys, "unknown prefix, empty subsys")
	u.Is("", suff, "unknown prefix, empty suffix")
	subsys, suff = subSystem(
		"loadbalancing.googleapis.com/https/backend_latencies", subs)
	u.Is("lb_https", subsys, "subsys for known prefix")
	u.Is("/backend_latencies", suff, "suffix for known prefix")
}

func fromJSON(p interface{}, text string) {
	if err := json.Unmarshal([]byte(text), p); nil != err {
		lager.Exit().Map(
			"Invalid json for", fmt.Sprintf("%T", p),
			"Error", err, "json", text)
	}
}

func TestMatcher(t *testing.T) {
	var u = tutl.New(t)

	ConfigFile = "../../gcp2prom.yaml"
	md := new(sd.MetricDescriptor)
	fromJSON(md, `{
		"description": "Some metric"
	  , "displayName": "Backend latency"
	  , "labels": [
			{
				"key": "protocol"
			  , "description": "Protocol used"
			}, {
				"key": "response_code"
			  , "description": "HTTP response code."
			  , "valueType": "INT64"
			}
		]
	  , "metadata": { "ingestDelay": "210s", "samplePeriod": "60s" }
	  , "metricKind": "DELTA"
	  , "type": "lowballing.gobbleapis.edu/bids/estimate"
	  , "unit": "ms"
	  , "valueType": "DISTRIBUTION"
	}`)

	cfg := MustLoadConfig("")
	u.Is(nil, cfg.MatchMetric(md), "unknown prefix")
	md.Type = "loadbalancing.googleapis.com/https/backend_latencies"
	mm := cfg.MatchMetric(md)

	// Selectors

	sel := Selector{}
	u.Is(true, mm.matches(sel), "match: blank")
	sel.Only = "F"
	u.Is(false, mm.matches(sel), "match: only F")
	sel.Only = "HF"
	u.Is(true, mm.matches(sel), "match: only HF")
	sel.Only = "H"
	u.Is(true, mm.matches(sel), "match: only H")

	fromJSON(&sel, `{"prefix": [ "lowball" ]}`)
	u.Is(false, mm.matches(sel), "no match w/ H: lowball*")
	sel.Only = ""
	u.Is(false, mm.matches(sel), "no match: lowball*")

	sel.Prefix = append(sel.Prefix, "loadbal")
	u.Is(true, mm.matches(sel), "match w/ H: loadbal*")
	sel.Only = ""
	u.Is(true, mm.matches(sel), "match: loadbal*")
	sel.Prefix[0], sel.Prefix[1] = sel.Prefix[1], sel.Prefix[0]
	sel.Only = "F"
	u.Is(false, mm.matches(sel), "no match w/ F: loadbal*")
	sel.Only = "FH"
	u.Is(true, mm.matches(sel), "match w/ FH: loadbal*")

	sel.Prefix = append(sel.Prefix, "bigquery")
	u.Is(true, mm.matches(sel), "match 2 w/ FH: loadbal*")
	sel.Prefix[0], sel.Prefix[2] = sel.Prefix[2], sel.Prefix[0]
	t.Log("last prefix list", sel.Prefix)
	sel.Only = ""
	u.Is(true, mm.matches(sel), "match 2: loadbal*")

//  u.Is("[_latencies rtt]", mm.conf.Suffix[1].keys, "precomputed keys")
	if u.IsNot(nil, mm, "known prefix") {
		u.Is("/backend_latency_seconds", mm.Name, "metric name")
		u.Is(
			"gcp_lb_https_backend_latency_seconds",
			mm.PromName(), "prom name")
		sf, _ := mm.Scaler()
		if u.IsNot(nil, sf, "has scaler") {
			u.Is(1.0, sf(1000.0), "scaler is milli")
		}
		minBuckets, minBound, minRatio, maxBound, maxBuckets :=
			mm.HistogramLimits()
		u.Is(24, minBuckets, "minBuckets")
		u.Is(0.01, minBound, "minBound")
		u.Is(1.9, minRatio, "minRatio")
		u.Is(600, maxBound, "maxBound")
		u.Is(0, maxBuckets, "maxBuckets")
		u.Is("[client_country proxy_continent]", mm.OmitLabels(), "omit labels")
	}
}
