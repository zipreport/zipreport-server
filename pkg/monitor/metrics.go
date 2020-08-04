package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	SuccessOps     prometheus.Counter
	FailedOps      prometheus.Counter
	ConversionTime prometheus.Histogram
}

func NewMetrics() *Metrics {
	return &Metrics{
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
