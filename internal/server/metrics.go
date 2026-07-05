package server

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the daemon's Prometheus collectors on a private registry (so the
// /metrics endpoint exposes exactly these, and tests avoid global-registry
// clashes).
type Metrics struct {
	registry       *prometheus.Registry
	requests       *prometheus.CounterVec
	duration       prometheus.Histogram
	inFlight       prometheus.Gauge
	activeSessions prometheus.Gauge
}

// NewMetrics builds and registers the daemon collectors.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudscraper_server_requests_total",
			Help: "Total fetch requests handled, by outcome (ok/http_error/error).",
		}, []string{"outcome"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "cloudscraper_server_request_duration_seconds",
			Help:    "Upstream fetch duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cloudscraper_server_in_flight_requests",
			Help: "Fetch requests currently in flight.",
		}),
		activeSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cloudscraper_server_active_sessions",
			Help: "Number of hot sessions currently held.",
		}),
	}
	m.registry.MustRegister(m.requests, m.duration, m.inFlight, m.activeSessions)
	return m
}

func (m *Metrics) observe(status int, err error, d time.Duration) {
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "error"
	case status >= 400:
		outcome = "http_error"
	}
	m.requests.WithLabelValues(outcome).Inc()
	if err == nil {
		m.duration.Observe(d.Seconds())
	}
}
