package querymw

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/promql/parser"
)

type ThanosClient interface {
	QueryInstant(context.Context, InstantRequest) error
	QueryRange(context.Context, RangeRequest) error
}

type Explanation struct {
	Name     string         `json:"name"`
	Children []*Explanation `json:"children,omitempty"`
}

type InstantRequest struct {
	next  http.Handler
	w     http.ResponseWriter
	Base  *url.URL
	Query string
	Opts  QueryOptions
	Time  time.Time
}

type RangeRequest struct {
	next  http.Handler
	w     http.ResponseWriter
	Base  *url.URL
	Query string
	Opts  QueryOptions
	Start time.Time
	End   time.Time
	Step  time.Duration
}

type QueryOptions struct {
	DoNotAddThanosParams bool        `json:"do_not_add_thanos_params,omitempty"`
	Deduplicate          bool        `json:"deduplicate,omitempty"`
	PartialResponse      bool        `json:"partial_response,omitempty"`
	Method               string      `json:"method,omitempty"`
	MaxSourceResolution  string      `json:"max_source_resolution,omitempty"`
	Engine               string      `json:"engine,omitempty"`
	HTTPHeaders          http.Header `json:"http_headers,omitempty"`
}

func (p *QueryOptions) AddTo(values url.Values) error {
	values.Add("dedup", strconv.FormatBool(p.Deduplicate))
	if p.MaxSourceResolution != "" {
		values.Add("max_source_resolution", p.MaxSourceResolution)
	}

	values.Add("engine", p.Engine)

	values.Add("partial_response", strconv.FormatBool(p.PartialResponse))

	return nil
}

type Config struct {
	EnableBackpressure        bool
	BackpressureMonitoringURL string
	BackpressureQueries       []string
	CongestionWindowMin       int
	CongestionWindowMax       int

	EnableJitter bool
	JitterDelay  time.Duration

	EnableObserver   bool
	ObserverRegistry *prometheus.Registry
}

func (c Config) Validate() error {
	if c.EnableJitter && c.JitterDelay == 0 {
		return ErrJitterDelayRequired
	}

	if c.EnableBackpressure {
		if len(c.BackpressureQueries) == 0 {
			return ErrBackpressureQueryRequired
		}
		for _, q := range c.BackpressureQueries {
			if _, err := parser.ParseExpr(q); err != nil {
				return err
			}
		}

		if c.BackpressureMonitoringURL == "" {
			return ErrBackpressureURLRequired
		}
		if _, err := url.Parse(c.BackpressureMonitoringURL); err != nil {
			return err
		}

		if c.CongestionWindowMin < 1 {
			return ErrCongestionWindowMinBelowOne
		}
		if c.CongestionWindowMax < c.CongestionWindowMin {
			return ErrCongestionWindowMaxBelowMin
		}
	}

	if c.EnableObserver && c.ObserverRegistry == nil {
		return ErrRegistryRequired
	}
	return nil
}

// NewMiddlewareFromConfig reads the middleware config to inject related queriers.
// Queriers are wrapped from last to first (when enabled) to
//
// 1. Parse *http.Request into the ThanosQuerier interface
//
// 2. Collect metrics on the internal queriers
//
// 3. Wait for some jitter to spread requests
//
// 4. Build *http.Request from ThanosQuerier interface and reconstruct response from http.Handler
func NewMiddlewareFromConfig(cfg Config) (*Entry, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var querier ThanosClient = Exit{}

	if cfg.EnableBackpressure {
		querier = NewBackpressure(querier, cfg.CongestionWindowMin, cfg.CongestionWindowMax, cfg.BackpressureQueries, cfg.BackpressureMonitoringURL)
	}

	if cfg.EnableJitter {
		querier = NewJitterer(querier, cfg.JitterDelay)
	}

	if cfg.EnableObserver {
		querier = NewObserver(querier, cfg.ObserverRegistry)
	}

	return &Entry{
		client: querier,
	}, nil
}

type Entry struct {
	client ThanosClient
}

func NewDefaultThanosMiddleware() *Entry {
	return &Entry{
		client: Exit{},
	}
}

func (t *Entry) InstantProxy(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		instant, err := instantFromRequest(next, w, r)
		if err != nil {
			prometheusAPIError(w, fmt.Sprintf("Failed to read instant request: %v", err), http.StatusInternalServerError)
			return
		}

		if err := t.client.QueryInstant(r.Context(), instant); err != nil {
			prometheusAPIError(w, fmt.Sprintf("Failed to process instant request: %v", err), http.StatusInternalServerError)
			return
		}
	})
}

func (t *Entry) RangeProxy(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := rangeFromRequest(next, w, r)
		if err != nil {
			prometheusAPIError(w, fmt.Sprintf("Failed to read range request: %v", err), http.StatusInternalServerError)
			return
		}

		if err := t.client.QueryRange(r.Context(), req); err != nil {
			prometheusAPIError(w, fmt.Sprintf("Failed to query range request: %v", err), http.StatusInternalServerError)
			return
		}
	})
}

type Exit struct{}

func (Exit) QueryInstant(ctx context.Context, req InstantRequest) error {
	r, err := requestFromInstant(ctx, req)
	if err != nil {
		return err
	}

	req.next.ServeHTTP(req.w, r)
	return nil
}

func (Exit) QueryRange(ctx context.Context, req RangeRequest) error {
	r, err := requestFromRange(ctx, req)
	if err != nil {
		return err
	}

	req.next.ServeHTTP(req.w, r)
	return nil
}
