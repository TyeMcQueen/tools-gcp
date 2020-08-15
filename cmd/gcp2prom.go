package main

import (
	"net/http"
	"os"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/tools-gcp/mon"
	"github.com/TyeMcQueen/tools-gcp/mon2prom"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)


func main() {
	if "" != os.Getenv("PANIC_ON_INT") {
		go tutl.ShowStackOnInterrupt()
	}
	monClient := mon.MustMonitoringClient(nil)
	proj := conn.DefaultProjectId()
	ch, runner := mon2prom.MetricFetcher(monClient)
	count := 0
	for _, pref := range config.GcpPrefixes() {
		for md := range monClient.StreamMetricDescs(nil, proj, pref) {
			if nil != mon2prom.NewVec(proj, monClient, md, ch) {
				count++
			}
		}
	}
	if 0 == count {
		lager.Exit().List("No metrics found to export.")
	}
	go runner()

	http.Handle("/metrics", promhttp.Handler())
	lager.Fail().Map("Can't listen", http.ListenAndServe(":8080", nil))
}
