// Package metrics exposes Prometheus metrics for the broadcast proxy.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics holds the Prometheus collectors registered by the proxy.
type Metrics struct {
	registry *prometheus.Registry

	RequestsTotal       *prometheus.CounterVec
	FanoutDuration      prometheus.Histogram
	TargetsPerBroadcast prometheus.GaugeFunc
	TargetRequests      *prometheus.CounterVec
}

// New builds and registers the metric set.
func New(targetCount func() float64) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: reg,
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "goxang",
			Subsystem: "broadcast",
			Name:      "requests_total",
			Help:      "Total number of broadcast requests handled by the proxy.",
		}, []string{"broadcast", "code"}),
		FanoutDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "goxang",
			Subsystem: "broadcast",
			Name:      "fanout_duration_seconds",
			Help:      "Wall-clock duration of the fan-out operation (capped by the configured timeout).",
			Buckets:   prometheus.ExponentialBuckets(0.0005, 2, 16),
		}),
		TargetRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "goxang",
			Subsystem: "broadcast",
			Name:      "target_requests_total",
			Help:      "Total number of requests fanned out to individual targets.",
		}, []string{"broadcast", "outcome"}),
		TargetsPerBroadcast: prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "goxang",
			Subsystem: "broadcast",
			Name:      "targets_total",
			Help:      "Current number of ready targets across all Broadcasts.",
		}, targetCount),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.FanoutDuration,
		m.TargetRequests,
		m.TargetsPerBroadcast,
	)
	return m
}

// Registry returns the Prometheus registry to expose on /metrics.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}
