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

package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	incident "github.com/incident-io/sdk-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeAPI serves canned incident.io responses. failIncidents makes the
// incidents endpoint answer 500.
type fakeAPI struct {
	failIncidents atomic.Bool
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	switch r.URL.Path {
	case "/v2/alert_sources":
		_, _ = w.Write([]byte(`{"alert_sources":[
			{"id":"as-prom","name":"alertmanager-infra-prod","source_type":"alertmanager"},
			{"id":"as-hb1","name":"heartbeat-infra-prod","source_type":"heartbeat","heartbeat_options":{"interval_seconds":60,"grace_period_seconds":120,"failure_threshold":1,"ping_url":"x"}},
			{"id":"as-hb2","name":"heartbeat-infra-dev","source_type":"heartbeat","heartbeat_options":{"interval_seconds":60,"grace_period_seconds":120,"failure_threshold":1,"ping_url":"x"}}
		]}`))
	case "/v2/alerts":
		if got := q.Get("status[one_of]"); got != "firing" {
			http.Error(w, "missing status filter: "+got, http.StatusBadRequest)
			return
		}
		// Two pages, to exercise pagination.
		if q.Get("after") == "" {
			_, _ = w.Write([]byte(`{"alerts":[
				{"id":"a1","alert_source_id":"as-prom","status":"firing","title":"t","created_at":"2026-11-25T10:00:00Z"},
				{"id":"a2","alert_source_id":"as-prom","status":"firing","title":"t","created_at":"2026-11-25T09:00:00Z"}
			],"pagination_meta":{"page_size":2,"after":"a2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"alerts":[
			{"id":"a3","alert_source_id":"as-hb1","status":"firing","title":"Missed heartbeat","created_at":"2026-11-25T11:00:00Z"}
		],"pagination_meta":{"page_size":2}}`))
	case "/v2/incidents":
		if f.failIncidents.Load() {
			http.Error(w, `{"type":"server_error"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"incidents":[
			{"id":"i1","reference":"INC-1","name":"Payments down","mode":"standard","created_at":"2026-11-25T08:00:00Z",
			 "incident_status":{"id":"s1","name":"Investigating","category":"live"},
			 "severity":{"id":"sev1","name":"Critical","rank":1},
			 "incident_type":{"id":"t1","name":"Default"}},
			{"id":"i2","reference":"INC-2","name":"Old one","mode":"standard","created_at":"2026-11-20T08:00:00Z",
			 "incident_status":{"id":"s2","name":"Post-incident","category":"learning"}}
		],"pagination_meta":{"page_size":50}}`))
	case "/v2/escalation_paths":
		_, _ = w.Write([]byte(`{"escalation_paths":[{"id":"ep1","name":"IPS SRE","current_responders":[{"id":"u1","name":"Alice"}]}],"pagination_meta":{"page_size":50}}`))
	case "/v2/escalations":
		_, _ = w.Write([]byte(`{"escalations":[
			{"id":"e1","title":"Disk full","status":"triggered","priority":{"name":"High"},"escalation_path_id":"ep1","created_at":"2026-11-25T11:30:00Z"},
			{"id":"e2","title":"CPU","status":"acked","priority":{"name":"Low"},"escalation_path_id":"ep1","created_at":"2026-11-25T11:00:00Z"},
			{"id":"e3","title":"Snoozed","status":"snoozed","priority":{"name":"Low"},"escalation_path_id":"ep1","created_at":"2026-11-25T10:00:00Z"}
		],"pagination_meta":{"page_size":50}}`))
	case "/v2/schedules":
		_, _ = w.Write([]byte(`{"schedules":[
			{"id":"sc1","name":"Primary","timezone":"Europe/Berlin","current_shifts":[
				{"start_at":"2026-11-25T00:00:00Z","end_at":"2026-11-26T00:00:00Z","rotation_id":"r1","user":{"id":"u1","name":"Alice"}}]},
			{"id":"sc2","name":"Empty","timezone":"UTC","current_shifts":[]}
		],"pagination_meta":{"page_size":25}}`))
	case "/v1/maintenance_windows":
		_, _ = w.Write([]byte(`{"maintenance_windows":[{"id":"mw1","name":"Upgrade","start_at":"2026-11-25T10:00:00Z","end_at":"2026-11-25T14:00:00Z"}],"pagination_meta":{"page_size":50}}`))
	case "/v3/teams":
		_, _ = w.Write([]byte(`{"teams":[{"id":"tm1","name":"IPS SRE","members":[{"id":"u1","name":"Alice"},{"id":"u2","name":"Bob"}]}],"pagination_meta":{"page_size":50}}`))
	case "/v1/severities":
		_, _ = w.Write([]byte(`{"severities":[{"id":"sev1","name":"Critical","rank":1}]}`))
	case "/v2/users":
		_, _ = w.Write([]byte(`{"users":[
			{"id":"u1","name":"Alice","is_active":true,"base_role":{"id":"r","name":"Responder","slug":"responder"}},
			{"id":"u3","name":"Gone","is_active":false,"base_role":{"id":"r","name":"Responder","slug":"responder"}}
		],"pagination_meta":{"page_size":50}}`))
	case "/v3/follow_ups":
		_, _ = w.Write([]byte(`{"follow_ups":[
			{"id":"f1","incident_id":"i1","title":"x","status":"outstanding","priority":{"id":"p","name":"High","rank":1}},
			{"id":"f2","incident_id":"i1","title":"y","status":"outstanding","priority":{"id":"p","name":"High","rank":1}},
			{"id":"f3","incident_id":"i1","title":"z","status":"completed"}
		],"pagination_meta":{"page_size":50}}`))
	default:
		http.NotFound(w, r)
	}
}

func newTestCollector(t *testing.T, f *fakeAPI) (*Collector, *prometheus.Registry) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := incident.New("test-key", incident.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	c := New(client, Options{FastInterval: time.Hour, SlowInterval: time.Hour, PollTimeout: 5 * time.Second, Version: "test"})
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	return c, reg
}

func TestCollectorSnapshot(t *testing.T) {
	c, reg := newTestCollector(t, &fakeAPI{})
	c.pollAll(context.Background(), true, true)

	expected := `
# HELP incidentio_alerts_firing Firing alerts, by alert source.
# TYPE incidentio_alerts_firing gauge
incidentio_alerts_firing{alert_source="alertmanager-infra-prod",alert_source_type="alertmanager"} 2
incidentio_alerts_firing{alert_source="heartbeat-infra-prod",alert_source_type="heartbeat"} 1
# HELP incidentio_heartbeat_expired 1 if the heartbeat alert source has a firing alert (it missed its pings).
# TYPE incidentio_heartbeat_expired gauge
incidentio_heartbeat_expired{alert_source="heartbeat-infra-dev"} 0
incidentio_heartbeat_expired{alert_source="heartbeat-infra-prod"} 1
# HELP incidentio_incidents Incidents not closed (triage, live, paused, learning), by status category, status, severity, type and mode.
# TYPE incidentio_incidents gauge
incidentio_incidents{incident_type="",mode="standard",severity="",status="Post-incident",status_category="learning"} 1
incidentio_incidents{incident_type="Default",mode="standard",severity="Critical",status="Investigating",status_category="live"} 1
# HELP incidentio_escalations Escalations (pages) not resolved, by status, priority and escalation path.
# TYPE incidentio_escalations gauge
incidentio_escalations{escalation_path="IPS SRE",priority="High",status="triggered"} 1
incidentio_escalations{escalation_path="IPS SRE",priority="Low",status="acked"} 1
incidentio_escalations{escalation_path="IPS SRE",priority="Low",status="snoozed"} 1
# HELP incidentio_schedule_oncall_users Number of users currently on call per schedule (0 when nobody is).
# TYPE incidentio_schedule_oncall_users gauge
incidentio_schedule_oncall_users{schedule_id="sc1",schedule_name="Primary"} 1
incidentio_schedule_oncall_users{schedule_id="sc2",schedule_name="Empty"} 0
# HELP incidentio_oncall_info 1 per user currently on call, per schedule.
# TYPE incidentio_oncall_info gauge
incidentio_oncall_info{rotation_id="r1",schedule_id="sc1",schedule_name="Primary",schedule_timezone="Europe/Berlin",user_id="u1",user_name="Alice"} 1
# HELP incidentio_users Active users, by base role.
# TYPE incidentio_users gauge
incidentio_users{base_role="Responder"} 1
# HELP incidentio_follow_ups Follow-ups by status and priority.
# TYPE incidentio_follow_ups gauge
incidentio_follow_ups{priority="",status="completed"} 1
incidentio_follow_ups{priority="High",status="outstanding"} 2
# HELP incidentio_maintenance_windows_active Number of maintenance windows currently active.
# TYPE incidentio_maintenance_windows_active gauge
incidentio_maintenance_windows_active 1
# HELP incidentio_team_members Number of members per team.
# TYPE incidentio_team_members gauge
incidentio_team_members{team_name="IPS SRE"} 2
# HELP incidentio_escalation_path_current_responders Number of users an escalation path would page right now (when the API returns it).
# TYPE incidentio_escalation_path_current_responders gauge
incidentio_escalation_path_current_responders{escalation_path="IPS SRE"} 1
# HELP incidentio_up 1 if the last poll of every enabled source succeeded.
# TYPE incidentio_up gauge
incidentio_up 1
`
	names := []string{
		"incidentio_alerts_firing", "incidentio_heartbeat_expired", "incidentio_incidents",
		"incidentio_escalations", "incidentio_schedule_oncall_users", "incidentio_oncall_info",
		"incidentio_users", "incidentio_follow_ups", "incidentio_maintenance_windows_active",
		"incidentio_team_members", "incidentio_escalation_path_current_responders", "incidentio_up",
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), names...); err != nil {
		t.Fatal(err)
	}

	// Per-incident series only for incidents still being handled.
	if n := testutil.CollectAndCount(c, "incidentio_incident_info"); n != 1 {
		t.Errorf("incident_info series = %d, want 1 (learning excluded)", n)
	}
	// Per-escalation series only for triggered and acknowledged pages.
	if n := testutil.CollectAndCount(c, "incidentio_escalation_created_timestamp_seconds"); n != 2 {
		t.Errorf("escalation_created series = %d, want 2", n)
	}
}

func TestFailingSourceKeepsPreviousData(t *testing.T) {
	f := &fakeAPI{}
	c, reg := newTestCollector(t, f)
	c.pollAll(context.Background(), true, true)

	f.failIncidents.Store(true)
	c.pollAll(context.Background(), false, true)

	expected := `
# HELP incidentio_source_up 1 if the last poll of the source succeeded.
# TYPE incidentio_source_up gauge
incidentio_source_up{source="alert_sources"} 1
incidentio_source_up{source="alerts"} 1
incidentio_source_up{source="escalation_paths"} 1
incidentio_source_up{source="escalations"} 1
incidentio_source_up{source="follow_ups"} 1
incidentio_source_up{source="incidents"} 0
incidentio_source_up{source="maintenance_windows"} 1
incidentio_source_up{source="oncall"} 1
incidentio_source_up{source="severities"} 1
incidentio_source_up{source="teams"} 1
incidentio_source_up{source="users"} 1
# HELP incidentio_up 1 if the last poll of every enabled source succeeded.
# TYPE incidentio_up gauge
incidentio_up 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "incidentio_source_up", "incidentio_up"); err != nil {
		t.Fatal(err)
	}
	// The incidents from the first poll are still served.
	if n := testutil.CollectAndCount(c, "incidentio_incidents"); n != 2 {
		t.Errorf("incidents series = %d, want 2 (kept from the last good poll)", n)
	}
	if v := testutil.ToFloat64(prometheus.CollectorFunc(func(ch chan<- prometheus.Metric) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		ch <- prometheus.MustNewConstMetric(c.d.sourceErrors, prometheus.CounterValue, c.state["incidents"].errors, "incidents")
	})); v != 1 {
		t.Errorf("incidents poll errors = %v, want 1", v)
	}
}

func TestDisabledSources(t *testing.T) {
	srv := httptest.NewServer(&fakeAPI{})
	t.Cleanup(srv.Close)
	client, err := incident.New("k", incident.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	c := New(client, Options{FastInterval: time.Hour, SlowInterval: time.Hour, PollTimeout: time.Second, Disabled: []string{"users", "follow_ups"}})
	c.pollAll(context.Background(), true, true)
	if n := testutil.CollectAndCount(c, "incidentio_users"); n != 0 {
		t.Errorf("users series = %d, want 0 (disabled)", n)
	}
	if n := testutil.CollectAndCount(c, "incidentio_source_up"); n != len(SourceNames())-2 {
		t.Errorf("source_up series = %d, want %d", n, len(SourceNames())-2)
	}
}
