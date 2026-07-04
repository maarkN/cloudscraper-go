package crawl

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the Prometheus collectors for a crawl. Construct with NewMetrics
// and pass it via Options.Metrics. A nil *Metrics disables recording.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics creates the collectors and registers them with reg (pass nil to
// skip registration — useful in tests that don't gather).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cloudscraper_crawl_requests_total",
			Help: "Total crawl fetches, labelled by host and outcome (ok/http_error/error).",
		}, []string{"host", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "cloudscraper_crawl_request_duration_seconds",
			Help:    "Crawl fetch duration in seconds, labelled by host.",
			Buckets: prometheus.DefBuckets,
		}, []string{"host"}),
	}
	if reg != nil {
		reg.MustRegister(m.requests, m.duration)
	}
	return m
}

func (m *Metrics) observe(host string, res Result) {
	outcome := "ok"
	switch {
	case res.Err != nil:
		outcome = "error"
	case res.StatusCode >= 400:
		outcome = "http_error"
	}
	m.requests.WithLabelValues(host, outcome).Inc()
	if res.Err == nil {
		m.duration.WithLabelValues(host).Observe(res.Duration.Seconds())
	}
}
