// Copyright 2026 Benjamin Zores
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gxben/incident-io-exporter/internal/api"
	"github.com/gxben/incident-io-exporter/internal/collector"
)

var version = "dev" // injected by ldflags at build time

func main() {
	var (
		listenAddr   = flag.String("web.listen-address", ":9429", "Address on which to expose metrics")
		metricsPath  = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
		apiKey       = flag.String("incidentio.api-key", "", "incident.io API key (or set INCIDENT_IO_API_KEY env var)")
		apiURL       = flag.String("incidentio.api-url", "", "incident.io API base URL (default: https://api.incident.io)")
		fastInterval = flag.Duration("incidentio.poll-interval", 60*time.Second, "Poll interval of incidents, alerts, escalations, on-call and maintenance windows")
		slowInterval = flag.Duration("incidentio.slow-poll-interval", 10*time.Minute, "Poll interval of users, teams, severities, alert sources, escalation paths and follow-ups")
		pollTimeout  = flag.Duration("incidentio.poll-timeout", 2*time.Minute, "Timeout of one source's poll, pagination and retries included")
		rateLimit    = flag.Float64("incidentio.rate-limit", 2, "Client-side API budget in requests per second (incident.io allows 1200 per minute per key); 0 disables it")
		rateBurst    = flag.Int("incidentio.rate-burst", 5, "Burst size of the client-side rate limiter")
		maxRetries   = flag.Int("incidentio.max-retries", 4, "Retries of a request on network errors, 429 and 5xx (Retry-After is honoured)")
		reqTimeout   = flag.Duration("incidentio.request-timeout", 30*time.Second, "Timeout of a single HTTP request")
		disabled     = flag.String("incidentio.disable-sources", "", "Comma-separated sources not to poll: "+strings.Join(collector.SourceNames(), ", "))
		logLevel     = flag.String("log.level", "info", "Log level: debug, info, warn, error")
		showVersion  = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("incident-io-exporter %s\n", version)
		os.Exit(0)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		slog.Error("invalid --log.level", "value", *logLevel)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// API key: flag takes precedence over env var.
	key := *apiKey
	if key == "" {
		key = os.Getenv("INCIDENT_IO_API_KEY")
	}
	if key == "" {
		slog.Error("incident.io API key is required (--incidentio.api-key or INCIDENT_IO_API_KEY)")
		os.Exit(1)
	}

	var disabledSources []string
	for s := range strings.SplitSeq(*disabled, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(collector.SourceNames(), s) {
			slog.Error("unknown source in --incidentio.disable-sources", "source", s, "known", collector.SourceNames())
			os.Exit(1)
		}
		disabledSources = append(disabledSources, s)
	}
	if *fastInterval <= 0 || *slowInterval <= 0 {
		slog.Error("poll intervals must be positive")
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	apiMetrics := api.NewMetrics("incidentio")
	reg.MustRegister(apiMetrics.Collectors()...)

	client, err := api.New(api.Config{
		APIKey:         key,
		BaseURL:        *apiURL,
		UserAgent:      "incident-io-exporter/" + version,
		RequestsPerSec: *rateLimit,
		Burst:          *rateBurst,
		MaxRetries:     *maxRetries,
		RetryWaitMin:   1 * time.Second,
		RetryWaitMax:   30 * time.Second,
		RequestTimeout: *reqTimeout,
	}, apiMetrics)
	if err != nil {
		slog.Error("failed to create incident.io client", "err", err)
		os.Exit(1)
	}

	coll := collector.New(client, collector.Options{
		FastInterval: *fastInterval,
		SlowInterval: *slowInterval,
		PollTimeout:  *pollTimeout,
		Disabled:     disabledSources,
		Version:      version,
	})
	reg.MustRegister(coll)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go coll.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<html>
<head><title>incident.io Exporter</title></head>
<body>
<h1>incident.io Exporter</h1>
<p>Version: %s</p>
<p><a href="%s">Metrics</a></p>
</body>
</html>`, version, *metricsPath)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	slog.Info("starting incident-io-exporter",
		"version", version,
		"listen", *listenAddr,
		"metrics_path", *metricsPath,
		"poll_interval", fastInterval.String(),
		"slow_poll_interval", slowInterval.String(),
		"rate_limit", *rateLimit,
	)

	// Scrapes only read the cached snapshot, so short server timeouts are fine.
	server := &http.Server{
		Addr:         *listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}
