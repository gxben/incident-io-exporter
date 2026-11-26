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

// Package collector polls the incident.io API in the background and serves
// the last good snapshot to Prometheus.
//
// Scrapes never call the API: each source is polled on its own cadence (fast
// for what changes by the minute, slow for reference data) and its metrics are
// replaced only when a poll succeeds, so a failing endpoint keeps serving its
// previous values while incidentio_source_last_success_timestamp_seconds tells
// how old they are. API load is therefore independent of the number of
// scrapers and of their interval.
package collector

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"time"

	incident "github.com/incident-io/sdk-go"
	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "incidentio"

// Options configure the collector.
type Options struct {
	FastInterval time.Duration // incidents, alerts, escalations, on-call, maintenance windows
	SlowInterval time.Duration // users, teams, severities, alert sources, escalation paths, follow-ups
	PollTimeout  time.Duration // upper bound for one source's poll, pagination included
	Disabled     []string      // source names not to poll
	Version      string
}

// source is one independently polled slice of the API.
type source struct {
	name string
	slow bool
	poll func(ctx context.Context) ([]prometheus.Metric, error)
}

// sourceState is what the last polls of a source left behind.
type sourceState struct {
	metrics     []prometheus.Metric // from the last successful poll
	success     bool                // outcome of the last poll
	duration    float64             // seconds, last poll
	lastSuccess time.Time
	errors      float64 // failed polls since start
	polled      bool
}

// Collector implements prometheus.Collector over the cached snapshot.
type Collector struct {
	api  incident.ClientWithResponsesInterface
	opts Options

	sources []source

	mu    sync.RWMutex
	state map[string]*sourceState

	// Reference data shared between sources (alert source names for alerts
	// and heartbeats, escalation path names for escalations), refreshed by
	// the slow sources.
	refMu           sync.RWMutex
	alertSources    map[string]alertSourceRef
	escalationPaths map[string]string

	d descs
}

// New returns a Collector. Call Run to start polling.
func New(api incident.ClientWithResponsesInterface, opts Options) *Collector {
	c := &Collector{
		api:             api,
		opts:            opts,
		state:           map[string]*sourceState{},
		alertSources:    map[string]alertSourceRef{},
		escalationPaths: map[string]string{},
		d:               newDescs(),
	}
	all := []source{
		// Slow first: the fast sources resolve names from them.
		{name: "alert_sources", slow: true, poll: c.pollAlertSources},
		{name: "escalation_paths", slow: true, poll: c.pollEscalationPaths},
		{name: "teams", slow: true, poll: c.pollTeams},
		{name: "severities", slow: true, poll: c.pollSeverities},
		{name: "users", slow: true, poll: c.pollUsers},
		{name: "follow_ups", slow: true, poll: c.pollFollowUps},
		{name: "incidents", poll: c.pollIncidents},
		{name: "alerts", poll: c.pollAlerts},
		{name: "escalations", poll: c.pollEscalations},
		{name: "oncall", poll: c.pollOnCall},
		{name: "maintenance_windows", poll: c.pollMaintenanceWindows},
	}
	for _, s := range all {
		if slices.Contains(opts.Disabled, s.name) {
			slog.Info("source disabled", "source", s.name)
			continue
		}
		c.sources = append(c.sources, s)
		c.state[s.name] = &sourceState{}
	}
	return c
}

// SourceNames lists every source the collector knows, for flag validation.
func SourceNames() []string {
	return []string{
		"alert_sources", "escalation_paths", "teams", "severities", "users", "follow_ups",
		"incidents", "alerts", "escalations", "oncall", "maintenance_windows",
	}
}

// Run polls every source once, then the fast and slow sources on their own
// tickers, until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	c.pollAll(ctx, true, true)

	fast := time.NewTicker(c.opts.FastInterval)
	slow := time.NewTicker(c.opts.SlowInterval)
	defer fast.Stop()
	defer slow.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-slow.C:
			c.pollAll(ctx, true, false)
		case <-fast.C:
			c.pollAll(ctx, false, true)
		}
	}
}

// pollAll polls the selected sources sequentially: the API budget is shared,
// so running them in parallel would only queue them on the rate limiter.
func (c *Collector) pollAll(ctx context.Context, slow, fast bool) {
	for _, s := range c.sources {
		if (s.slow && !slow) || (!s.slow && !fast) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		c.pollOne(ctx, s)
	}
}

func (c *Collector) pollOne(parent context.Context, s source) {
	ctx, cancel := context.WithTimeout(parent, c.opts.PollTimeout)
	defer cancel()

	start := time.Now()
	metrics, err := s.poll(ctx)
	elapsed := time.Since(start).Seconds()

	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.state[s.name]
	st.polled = true
	st.duration = elapsed
	if err != nil {
		st.success = false
		st.errors++
		if !errors.Is(err, context.Canceled) {
			slog.Warn("poll failed, keeping previous data", "source", s.name, "err", err, "duration", elapsed)
		}
		return
	}
	st.success = true
	st.lastSuccess = time.Now()
	st.metrics = metrics
	slog.Debug("poll succeeded", "source", s.name, "metrics", len(metrics), "duration", elapsed)
}

// Describe implements prometheus.Collector. The collector is unchecked: the
// set of label values depends on the API's data.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.d.buildInfo, prometheus.GaugeValue, 1, c.opts.Version, runtime.Version())

	c.mu.RLock()
	defer c.mu.RUnlock()

	up := 1.0
	for _, s := range c.sources {
		st := c.state[s.name]
		for _, m := range st.metrics {
			ch <- m
		}
		if !st.polled {
			up = 0
			continue
		}
		ok := 0.0
		if st.success {
			ok = 1
		} else {
			up = 0
		}
		ch <- prometheus.MustNewConstMetric(c.d.sourceUp, prometheus.GaugeValue, ok, s.name)
		ch <- prometheus.MustNewConstMetric(c.d.sourceDuration, prometheus.GaugeValue, st.duration, s.name)
		ch <- prometheus.MustNewConstMetric(c.d.sourceErrors, prometheus.CounterValue, st.errors, s.name)
		if !st.lastSuccess.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.d.sourceLastSuccess, prometheus.GaugeValue, float64(st.lastSuccess.Unix()), s.name)
		}
	}
	ch <- prometheus.MustNewConstMetric(c.d.up, prometheus.GaugeValue, up)
}
