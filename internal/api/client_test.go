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

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testConfig(url string) Config {
	return Config{
		APIKey:         "test-key",
		BaseURL:        url,
		RequestsPerSec: 0, // unlimited unless a test sets it
		Burst:          1,
		MaxRetries:     3,
		RetryWaitMin:   10 * time.Millisecond,
		RetryWaitMax:   50 * time.Millisecond,
		RequestTimeout: 5 * time.Second,
	}
}

const severitiesBody = `{"severities":[{"id":"01","name":"Critical","rank":1}]}`

func TestRetriesOn429HonouringRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(severitiesBody))
	}))
	defer srv.Close()

	m := NewMetrics("test")
	c, err := New(testConfig(srv.URL), m)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	resp, err := c.SeveritiesV1ListWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON200 == nil || len(resp.JSON200.Severities) != 1 {
		t.Fatalf("unexpected response %d: %s", resp.StatusCode(), resp.Body)
	}
	// Retry-After: 1 wins over RetryWaitMax (50ms).
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retried after %s, want >= 1s (Retry-After)", elapsed)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if v := testutil.ToFloat64(m.RateLimited.WithLabelValues("/v1/severities")); v != 1 {
		t.Errorf("rate_limited_total = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.Requests.WithLabelValues("/v1/severities", "429")); v != 1 {
		t.Errorf("requests_total{code=429} = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.Requests.WithLabelValues("/v1/severities", "200")); v != 1 {
		t.Errorf("requests_total{code=200} = %v, want 1", v)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.MaxRetries = 2
	c, err := New(cfg, NewMetrics("test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SeveritiesV1ListWithResponse(context.Background()); err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (1 + 2 retries)", calls.Load())
	}
}

func TestClientSideRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(severitiesBody))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	cfg.RequestsPerSec = 10 // one token every 100ms
	cfg.Burst = 1
	c, err := New(cfg, NewMetrics("test"))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for range 4 {
		if _, err := c.SeveritiesV1ListWithResponse(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// First request immediate, the next three wait ~100ms each.
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("4 requests took %s, want >= ~300ms at 10 req/s", elapsed)
	}
}

func TestAPIKeyRequired(t *testing.T) {
	if _, err := New(Config{}, nil); err == nil {
		t.Fatal("expected an error without API key")
	}
}
