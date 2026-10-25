package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	HttpServers    prometheus.Gauge
	Browsers       prometheus.Gauge
	TotalOps       prometheus.Counter
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
		TotalOps: promauto.NewCounter(prometheus.CounterOpts{
			Name: "total_requests",
			Help: "Total conversion requests",
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
			// must cover render.JobMaxTimeout (600 s)
			Buckets: []float64{1, 2, 5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300, 450, 600},
		}),
	}
}
