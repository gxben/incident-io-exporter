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

// Package api builds the incident.io SDK client used by the exporter: a
// client-side rate limiter, retries honouring Retry-After, and per-endpoint
// request metrics, all wired below the SDK through its WithHTTPClient option.
package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	incident "github.com/incident-io/sdk-go"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

// Config holds the API client settings.
type Config struct {
	APIKey         string
	BaseURL        string        // empty = incident.DefaultEndpoint
	UserAgent      string        // e.g. incident-io-exporter/1.0.0
	RequestsPerSec float64       // client-side budget, well below the API's 1200 req/min per key
	Burst          int           // token bucket size
	MaxRetries     int           // retries on network errors, 429 and 5xx
	RetryWaitMin   time.Duration // lower bound of the exponential backoff
	RetryWaitMax   time.Duration // upper bound of the exponential backoff (a Retry-After header wins)
	RequestTimeout time.Duration // per HTTP attempt
}

// Metrics are the API client's own metrics, registered by the caller.
type Metrics struct {
	Requests    *prometheus.CounterVec   // by endpoint and HTTP status code ("error" for transport failures)
	Duration    *prometheus.HistogramVec // by endpoint
	RateLimited *prometheus.CounterVec   // 429 answers, by endpoint
	Throttled   prometheus.Histogram     // time spent waiting on the client-side limiter
}

// NewMetrics returns the client metrics under the given namespace.
func NewMetrics(namespace string) *Metrics {
	return &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "api", Name: "requests_total",
			Help: "HTTP requests sent to the incident.io API, by endpoint and status code (each retry counts).",
		}, []string{"endpoint", "code"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "api", Name: "request_duration_seconds",
			Help:    "Duration of HTTP requests to the incident.io API, by endpoint.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"endpoint"}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "api", Name: "rate_limited_total",
			Help: "Requests answered 429 Too Many Requests by the incident.io API, by endpoint.",
		}, []string{"endpoint"}),
		Throttled: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "api", Name: "throttle_wait_seconds",
			Help:    "Time requests waited for the client-side rate limiter before being sent.",
			Buckets: []float64{.01, .1, .5, 1, 2, 5, 10, 30},
		}),
	}
}

// Collectors returns the metrics to register.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Requests, m.Duration, m.RateLimited, m.Throttled}
}

// New returns an incident.io SDK client. The HTTP stack, from the SDK down:
//
//	retryablehttp (backoff, Retry-After on 429/503)
//	  -> limitedTransport (token bucket, then metrics)
//	    -> http.DefaultTransport
//
// The limiter sits below the retry loop, so retries spend the same budget as
// first attempts and a burst of retries can't exceed it.
func New(cfg Config, m *Metrics) (*incident.ClientWithResponses, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("incident.io API key is required")
	}
	limit := rate.Inf
	if cfg.RequestsPerSec > 0 {
		limit = rate.Limit(cfg.RequestsPerSec)
	}
	burst := max(cfg.Burst, 1)

	rc := retryablehttp.NewClient()
	rc.Logger = nil
	rc.RetryMax = cfg.MaxRetries
	rc.RetryWaitMin = cfg.RetryWaitMin
	rc.RetryWaitMax = cfg.RetryWaitMax
	// DefaultBackoff: exponential between RetryWaitMin and RetryWaitMax, but a
	// Retry-After header on a 429 or 503 is honoured as given.
	rc.Backoff = retryablehttp.DefaultBackoff
	rc.HTTPClient = &http.Client{
		Timeout: cfg.RequestTimeout,
		Transport: &limitedTransport{
			next:    http.DefaultTransport,
			limiter: rate.NewLimiter(limit, burst),
			metrics: m,
		},
	}

	opts := []incident.ClientOption{incident.WithHTTPClient(rc.StandardClient())}
	if cfg.UserAgent != "" {
		opts = append(opts, incident.WithUserAgent(cfg.UserAgent))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, incident.WithBaseURL(cfg.BaseURL))
	}
	return incident.New(cfg.APIKey, opts...)
}

// limitedTransport waits for the rate limiter, sends the request and records
// its outcome.
type limitedTransport struct {
	next    http.RoundTripper
	limiter *rate.Limiter
	metrics *Metrics
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	endpoint := req.URL.Path
	waitStart := time.Now()
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, fmt.Errorf("rate limiter: %w", err)
	}
	if t.metrics != nil {
		t.metrics.Throttled.Observe(time.Since(waitStart).Seconds())
	}

	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	if t.metrics != nil {
		t.metrics.Duration.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
		code := "error"
		if err == nil {
			code = strconv.Itoa(resp.StatusCode)
			if resp.StatusCode == http.StatusTooManyRequests {
				t.metrics.RateLimited.WithLabelValues(endpoint).Inc()
			}
		}
		t.metrics.Requests.WithLabelValues(endpoint, code).Inc()
	}
	return resp, err
}
