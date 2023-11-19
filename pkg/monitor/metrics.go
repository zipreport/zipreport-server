package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	HttpServers    prometheus.Gauge
	Browsers       prometheus.Gauge
	SuccessOps     prometheus.Counter
	FailedOps      prometheus.Counter
	ConversionTime prometheus.Histogram
}

func NewMetrics() *Metrics {
	return &Metrics{
		HttpServers: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "current_http_servers",
			Help: "Current http server count",
		}),
		Browsers: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "current_browsers",
			Help: "Current browser instances count",
		}),
		SuccessOps: promauto.NewCounter(prometheus.CounterOpts{
			Name: "total_request_success",
			Help: "Total successful conversion requests",
		}),
		FailedOps: promauto.NewCounter(prometheus.CounterOpts{
			Name: "total_request_error",
			Help: "Total failed conversion requests",
		}),
		ConversionTime: promauto.NewHistogram(prometheus.HistogramOpts{
			Name: "conversion_time",
			Help: "PDF conversion time, in seconds.",
			// count should be based on render.JobProcessTimeout
			Buckets: prometheus.LinearBuckets(1, 9, 13),
		}),
	}
}

func (m *Metrics) IncHttpServers() {
	if m != nil {
		m.HttpServers.Inc()
	}
}

func (m *Metrics) DecHttpServers() {
	if m != nil {
		m.HttpServers.Dec()
	}
}

func (m *Metrics) IncBrowsers() {
	if m != nil {
		m.Browsers.Inc()
	}
}

func (m *Metrics) DecBrowsers() {
	if m != nil {
		m.Browsers.Dec()
	}
}

func (m *Metrics) IncFailed() {
	if m != nil {
		m.FailedOps.Inc()
	}
}

func (m *Metrics) IncSuccess() {
	if m != nil {
		m.SuccessOps.Inc()
	}
}

func (m *Metrics) ObserveConversionTime(v float64) {
	if m != nil {
		m.ConversionTime.Observe(v)
	}
}
