// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package querymw

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/efficientgo/core/merrors"
	"github.com/metalmatze/signal/server/signalhttp"
	"github.com/prometheus/client_golang/prometheus"
)

type routes struct {
	upstream *url.URL
	handler  http.Handler

	mux http.Handler

	logger *log.Logger

	mw *Entry
}

type options struct {
	passthroughPaths []string
	registerer       prometheus.Registerer
}

type Option interface {
	apply(*options)
}

type optionFunc func(*options)

func (f optionFunc) apply(o *options) {
	f(o)
}

// WithPrometheusRegistry configures the proxy to use the given registerer.
func WithPrometheusRegistry(reg prometheus.Registerer) Option {
	return optionFunc(func(o *options) {
		o.registerer = reg
	})
}

// WithPassthroughPaths configures routes to register given paths as passthrough handlers for all HTTP methods.
// that, if requested, will be forwarded without enforcing label. Use with care.
// NOTE: Passthrough "all" paths like "/" or "" and regex are not allowed.
func WithPassthroughPaths(paths []string) Option {
	return optionFunc(func(o *options) {
		o.passthroughPaths = paths
	})
}

// mux abstracts away the behavior we expect from the http.ServeMux type in this package.
type mux interface {
	http.Handler
	Handle(string, http.Handler)
}

// strictMux is a mux that wraps standard HTTP handler with safer handler that allows safe user provided handler registrations.
type strictMux struct {
	mux
	seen map[string]struct{}
}

func newStrictMux(m mux) *strictMux {
	return &strictMux{
		m,
		map[string]struct{}{},
	}

}

// Handle is like HTTP mux handle but it does not allow to register paths that are shared with previously registered paths.
// It also makes sure the trailing / is registered too.
func (s *strictMux) Handle(pattern string, handler http.Handler) error {
	sanitized := pattern
	for next := strings.TrimSuffix(sanitized, "/"); next != sanitized; sanitized = next {
	}

	if _, ok := s.seen[sanitized]; ok {
		return fmt.Errorf("pattern %q was already registered", sanitized)
	}

	for p := range s.seen {
		if strings.HasPrefix(sanitized+"/", p+"/") {
			return fmt.Errorf("pattern %q is registered, cannot register path %q that shares it", p, sanitized)
		}
	}

	s.mux.Handle(sanitized, handler)
	s.mux.Handle(sanitized+"/", handler)
	s.seen[sanitized] = struct{}{}

	return nil
}

// instrumentedMux wraps a mux and instruments it.
type instrumentedMux struct {
	mux
	i signalhttp.HandlerInstrumenter
}

func newInstrumentedMux(m mux, r prometheus.Registerer) *instrumentedMux {
	return &instrumentedMux{
		m,
		signalhttp.NewHandlerInstrumenter(r, []string{"handler"}),
	}
}

// Handle implements the mux interface.
func (i *instrumentedMux) Handle(pattern string, handler http.Handler) {
	i.mux.Handle(pattern, i.i.NewHandler(prometheus.Labels{"handler": pattern}, handler))
}

func NewRoutes(mw *Entry, upstream *url.URL, opts ...Option) (*routes, error) {
	opt := options{}
	for _, o := range opts {
		o.apply(&opt)
	}

	if opt.registerer == nil {
		opt.registerer = prometheus.NewRegistry()
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)

	r := &routes{
		upstream: upstream,
		handler:  proxy,
		logger:   log.Default(),
		mw:       mw,
	}
	mux := newStrictMux(newInstrumentedMux(http.NewServeMux(), opt.registerer))

	errs := merrors.New(
		mux.Handle("/api/v1/query", r.mw.InstantProxy(r.passthrough)),
		mux.Handle("/api/v1/query_range", r.mw.RangeProxy(r.passthrough)),

		mux.Handle("/federate", http.HandlerFunc(r.passthrough)),
		mux.Handle("/graph", http.HandlerFunc(r.passthrough)),
		mux.Handle("/ui", http.HandlerFunc(r.passthrough)),
		mux.Handle("/api/v1/alerts", http.HandlerFunc(r.passthrough)),
		mux.Handle("/api/v1/rules", http.HandlerFunc(r.passthrough)),
		mux.Handle("/api/v1/series", http.HandlerFunc(r.passthrough)),
		mux.Handle("/api/v1/query_exemplars", http.HandlerFunc(r.passthrough)),
	)

	errs.Add(
		mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		})),
	)

	if err := errs.Err(); err != nil {
		return nil, err
	}

	// Validate paths.
	for _, path := range opt.passthroughPaths {
		u, err := url.Parse(fmt.Sprintf("http://example.com%v", path))
		if err != nil {
			return nil, fmt.Errorf("path %q is not a valid URI path, got %v", path, opt.passthroughPaths)
		}
		if u.Path != path {
			return nil, fmt.Errorf("path %q is not a valid URI path, got %v", path, opt.passthroughPaths)
		}
		if u.Path == "" || u.Path == "/" {
			return nil, fmt.Errorf("path %q is not allowed, got %v", u.Path, opt.passthroughPaths)
		}
	}

	// Register optional passthrough paths.
	for _, path := range opt.passthroughPaths {
		if err := mux.Handle(path, http.HandlerFunc(r.passthrough)); err != nil {
			return nil, err
		}
	}

	r.mux = mux
	proxy.ErrorHandler = r.errorHandler
	proxy.ErrorLog = log.Default()

	return r, nil
}

func (r *routes) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *routes) errorHandler(rw http.ResponseWriter, _ *http.Request, err error) {
	r.logger.Printf("http: proxy error: %v", err)
	rw.WriteHeader(http.StatusBadGateway)
}

func (r *routes) passthrough(w http.ResponseWriter, req *http.Request) {
	r.handler.ServeHTTP(w, req)
}
