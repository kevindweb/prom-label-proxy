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

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/metalmatze/signal/internalserver"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/prometheus-community/prom-label-proxy/querymw"
)

func main() {
	var (
		insecureListenAddress  string
		internalListenAddress  string
		upstream               string
		unsafePassthroughPaths string // Comma-delimited string.

		enableBackpressure        bool
		backpressureMonitoringURL string
		backpressureQueries       string
		congestionWindowMin       int
		congestionWindowMax       int

		enableJitter bool
		jitterDelay  time.Duration

		enableObserver bool
	)

	flagset := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flagset.StringVar(&insecureListenAddress, "insecure-listen-address", "", "The address the prom-label-proxy HTTP server should listen on.")
	flagset.StringVar(&internalListenAddress, "internal-listen-address", "", "The address the internal prom-label-proxy HTTP server should listen on to expose metrics about itself.")
	flagset.StringVar(&upstream, "upstream", "", "The upstream URL to proxy to.")
	flagset.BoolVar(&enableJitter, "enable-jitter", false, "Use the jitter middleware")
	flagset.DurationVar(&jitterDelay, "jitter-delay", time.Second, "Random jitter to apply when enabled")
	flagset.BoolVar(&enableBackpressure, "enable-backpressure", false, "Use the additive increase multiplicative decrease middleware using backpressure metrics")
	flagset.IntVar(&congestionWindowMin, "backpressure-min-window", 0, "Min concurrent queries to passthrough regardless of spikes in backpressure.")
	flagset.IntVar(&congestionWindowMax, "backpressure-max-window", 0, "Max concurrent queries to passthrough regardless of backpressure health.")
	flagset.StringVar(&backpressureMonitoringURL, "backpressure-monitoring-url", "", "The address on which to read backpressure metrics with PromQL queries.")
	flagset.StringVar(&backpressureQueries, "backpressure-queries", "", "Newline separated allow list of queries that signifiy increase in downstream failure. Will be used to reduce congestion window. "+
		"Queries should be in the form of `sum(rate(throughput[5m])) > 100tbps` where an empty result means no backpressure is occuring")
	flagset.BoolVar(&enableObserver, "enable-observer", false, "Collect middleware latency and error metrics")
	flagset.StringVar(&unsafePassthroughPaths, "unsafe-passthrough-paths", "", "Comma delimited allow list of exact HTTP path segments that should be allowed to hit upstream URL without any enforcement. "+
		"This option is checked after Prometheus APIs, you cannot override enforced API endpoints to be not enforced with this option. Use carefully as it can easily cause a data leak if the provided path is an important "+
		"API (like /api/v1/configuration) which isn't enforced by prom-label-proxy. NOTE: \"all\" matching paths like \"/\" or \"\" and regex are not allowed.")

	//nolint: errcheck // Parse() will exit on error.
	flagset.Parse(os.Args[1:])

	upstreamURL, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("Failed to build parse upstream URL: %v", err)
	}

	if upstreamURL.Scheme != "http" && upstreamURL.Scheme != "https" {
		log.Fatalf("Invalid scheme for upstream URL %q, only 'http' and 'https' are supported", upstream)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	opts := []querymw.Option{querymw.WithPrometheusRegistry(reg)}

	if len(unsafePassthroughPaths) > 0 {
		opts = append(opts, querymw.WithPassthroughPaths(strings.Split(unsafePassthroughPaths, ",")))
	}

	cfg := querymw.Config{
		EnableBackpressure:        enableBackpressure,
		BackpressureMonitoringURL: backpressureMonitoringURL,
		BackpressureQueries:       strings.Split(backpressureQueries, "\n"),
		CongestionWindowMin:       congestionWindowMin,
		CongestionWindowMax:       congestionWindowMax,

		EnableJitter: enableJitter,
		JitterDelay:  jitterDelay,

		EnableObserver:   enableObserver,
		ObserverRegistry: reg,
	}
	mw, err := querymw.NewMiddlewareFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create middleware from config: %v", err)
	}

	var g run.Group

	{
		// Run the insecure HTTP server.
		routes, err := querymw.NewRoutes(mw, upstreamURL, opts...)
		if err != nil {
			log.Fatalf("Failed to create querymw Routes: %v", err)
		}

		mux := http.NewServeMux()
		mux.Handle("/", routes)

		l, err := net.Listen("tcp", insecureListenAddress)
		if err != nil {
			log.Fatalf("Failed to listen on insecure address: %v", err)
		}

		srv := &http.Server{Handler: mux}

		g.Add(func() error {
			log.Printf("Listening insecurely on %v", l.Addr())
			if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
				log.Printf("Server stopped with %v", err)
				return err
			}
			return nil
		}, func(error) {
			srv.Close()
		})
	}

	if internalListenAddress != "" {
		// Run the internal HTTP server.
		h := internalserver.NewHandler(
			internalserver.WithName("Internal prom-label-proxy API"),
			internalserver.WithPrometheusRegistry(reg),
			internalserver.WithPProf(),
		)
		// Run the HTTP server.
		l, err := net.Listen("tcp", internalListenAddress)
		if err != nil {
			log.Fatalf("Failed to listen on internal address: %v", err)
		}

		srv := &http.Server{Handler: h}

		g.Add(func() error {
			log.Printf("Listening on %v for metrics and pprof", l.Addr())
			if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
				log.Printf("Internal server stopped with %v", err)
				return err
			}
			return nil
		}, func(error) {
			srv.Close()
		})
	}

	g.Add(run.SignalHandler(context.Background(), syscall.SIGINT, syscall.SIGTERM))

	if err := g.Run(); err != nil {
		if !errors.As(err, &run.SignalError{}) {
			log.Printf("Server stopped with %v", err)
			os.Exit(1)
		}
		log.Print("Caught signal; exiting gracefully...")
	}
}
