package metric

import (
	"os"
	"sync"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/prometheus/client_golang/prometheus"
)

// First component of metric names:
var _System = func() string {
	if sys := os.Getenv("PROM_SYSTEM"); "" != sys {
		return sys
	}
	return "capacity"
}()

// Label names used in these metrics:
var keys = []string{"resource", "domain", "period"}

var _MinGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: _System,
		Name:      "min_utilization",
		Help:      "Minimum resource utilization (from 0 to 1) over recent time period",
	},
	keys,
)

var _MaxGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: _System,
		Name:      "max_utilization",
		Help:      "Maximum resource utilization (from 0 to 1) over recent time period",
	},
	keys,
)

// A CapacityUsage uses two Prometheus gauge metrics, by default,
// "capacity_min_utilization" and "capacity_max_utilization".  They represent
// the minimum/maximum resource utilization over the recent period of time.
// Each value will be between 0.0 and 1.0.  They each have three labels:
// "resource", "domain", and "period".
//
// The "capacity" prefix to the metric names can be changed by setting the
// "PROM_SYSTEM" environment variable.
//
type CapacityUsage struct {
	mu  sync.Mutex
	cap float64
	min float64
	max float64
	pn  prometheus.Gauge
	px  prometheus.Gauge
	dur time.Duration
	end time.Time
	lab []string
}

// MustRegister() registers the CapacityUsage metrics so they can be scraped
// by Prometheus, exiting on failure.
//
func MustRegister(reg prometheus.Registerer) {
	err := Register(reg)
	if nil != err {
		lager.Exit().MMap(
			"Could not register Prometheus metrics for CapacityUsage",
			"error", err)
	}
}

// Register() registers the CapacityUsage metrics so they can be scraped by
// Prometheus, returning an error on failure.
//
func Register(reg prometheus.Registerer) error {
	if nil == reg {
		reg = prometheus.DefaultRegisterer
	}
	err := reg.Register(_MinGauge)
	if nil == err {
		err = reg.Register(_MaxGauge)
	}
	return err
}

// NewCapacityUsage() returns a CapacityUsage with a new set of labels
// to record the utilization of a limited resource where this utilization
// might spike for a short period and so a single Prometheus Gauge is not
// sufficient to capture such spikes.
//
// 'capacity' is the quantity of the limited resource (e.g. the
// number of slots).  'resource' is the name of the resource (e.g.
// "span-creation-backlog"), 'domain' is the domain in which the resource
// resides (e.g. a workload name), and 'interval' is a string representing
// a time.Duration over which min and max utilization are measured.
//
// metric.Register() or metric.MustRegister() must be called for these
// metrics to be exposed to be scraped by Prometheus.
//
func NewCapacityUsage(
	capacity float64, resource, domain, interval string,
) (*CapacityUsage, error) {
	dur, err := time.ParseDuration(interval)
	if nil != err {
		return nil, err
	}
	m := &CapacityUsage{
		cap: capacity,
		dur: dur,
		lab: []string{resource, domain, interval},
	}
	return m, nil
}

// Record() records the current usage level of the limited resource.
// 'used' should be a value between 0 and the 'capacity' value passed to
// NewCapacityUsage().  Division is done to change this number to be a
// utilization level, a number between 0.0 and 1.0.
//
func (m *CapacityUsage) Record(used float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	reset := false
	if nil == m.pn { // First call to Record():
		m.end = now
		reset = true
	} else if now.After(m.end) {
		reset = true
	}
	utilization := used / m.cap
	if reset {
		periods := 1 + now.Sub(m.end)/m.dur
		m.end = m.end.Add(periods * m.dur)
		m.min = utilization
		m.max = utilization
		m.pn = _MinGauge.WithLabelValues(m.lab...)
		m.pn.Set(utilization)
		m.px = _MaxGauge.WithLabelValues(m.lab...)
		m.px.Set(utilization)
		return
	}
	if utilization < m.min {
		m.min = utilization
		m.pn.Set(utilization)
	}
	if m.max < utilization {
		m.max = utilization
		m.px.Set(utilization)
	}
}
