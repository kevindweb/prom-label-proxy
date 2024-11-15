package querymw

import (
	"context"
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	errCountMetric   = "querymw_error_count"
	blockCountMetric = "querymw_block_count"
	reqCountMetric   = "querymw_request_count"
	latencyMetric    = "querymw_request_latency_ms"
)

// Observer emits metrics such as error rate and how often queriers are blocking requests.
// Each querier that blocks requests should tag their errors with a querier type to filter metrics.
type Observer struct {
	now    func() time.Time
	since  func(time.Time) time.Duration
	client ThanosClient

	errCounter     *prometheus.CounterVec
	blockCounter   *prometheus.CounterVec
	reqCounter     *prometheus.CounterVec
	latencyCounter *prometheus.CounterVec
}

var _ ThanosClient = &Observer{}

func NewObserver(querier ThanosClient, reg *prometheus.Registry) *Observer {
	o := &Observer{
		now:    time.Now,
		since:  time.Since,
		client: querier,

		errCounter:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: errCountMetric}, []string{"query_type"}),
		blockCounter:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: blockCountMetric}, []string{"query_type", "mw_type"}),
		reqCounter:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: reqCountMetric}, []string{"query_type"}),
		latencyCounter: prometheus.NewCounterVec(prometheus.CounterOpts{Name: latencyMetric}, []string{"query_type"}),
	}

	reg.MustRegister(o.errCounter, o.blockCounter, o.reqCounter, o.latencyCounter)
	return o
}

func (o *Observer) QueryInstant(ctx context.Context, r InstantRequest) error {
	start := o.now()
	err := o.client.QueryInstant(ctx, r)
	o.handleMetrics(err, start, "instant")
	return err
}

func (o *Observer) QueryRange(ctx context.Context, r RangeRequest) error {
	start := o.now()
	err := o.client.QueryRange(ctx, r)
	o.handleMetrics(err, start, "range")
	return err
}

func (o *Observer) handleMetrics(err error, start time.Time, queryType string) {
	if err != nil {
		var blocked *RequestBlockedError
		if !errors.As(err, &blocked) {
			o.blockCounter.WithLabelValues(queryType, blocked.Type).Inc()
		} else {
			o.errCounter.WithLabelValues(queryType).Inc()
		}
	}

	o.reqCounter.WithLabelValues(queryType).Inc()
	o.latencyCounter.WithLabelValues(queryType).Add(float64(o.since(start).Milliseconds()))
}
