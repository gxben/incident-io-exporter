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

import "github.com/prometheus/client_golang/prometheus"

type descs struct {
	// exporter
	buildInfo, up                                             *prometheus.Desc
	sourceUp, sourceDuration, sourceErrors, sourceLastSuccess *prometheus.Desc
	// incidents
	incidents, incidentInfo, incidentCreated *prometheus.Desc
	// alerts and heartbeats
	alertsFiring, alertOldestFiring     *prometheus.Desc
	heartbeatExpired, heartbeatInterval *prometheus.Desc
	// escalations (pages)
	escalations, escalationCreated *prometheus.Desc
	// on-call
	scheduleInfo, scheduleOnCallUsers, oncallInfo, oncallShiftEnd *prometheus.Desc
	// maintenance windows
	maintenanceWindowsActive, maintenanceWindowEnd *prometheus.Desc
	// reference data
	alertSourceInfo, alertSources                *prometheus.Desc
	escalationPathInfo, escalationPathResponders *prometheus.Desc
	teamInfo, teamMembers, teams                 *prometheus.Desc
	severityInfo                                 *prometheus.Desc
	userInfo, users                              *prometheus.Desc
	followUps                                    *prometheus.Desc
}

func newDescs() descs {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
	}
	return descs{
		buildInfo:         d("exporter_build_info", "Exporter build information.", "version", "goversion"),
		up:                d("up", "1 if the last poll of every enabled source succeeded."),
		sourceUp:          d("source_up", "1 if the last poll of the source succeeded.", "source"),
		sourceDuration:    d("source_poll_duration_seconds", "Duration of the last poll of the source, pagination and retries included.", "source"),
		sourceErrors:      d("source_poll_errors_total", "Failed polls of the source since the exporter started.", "source"),
		sourceLastSuccess: d("source_last_success_timestamp_seconds", "Unix time of the last successful poll of the source: the age of the data it serves.", "source"),

		incidents:       d("incidents", "Incidents not closed (triage, live, paused, learning), by status category, status, severity, type and mode.", "status_category", "status", "severity", "incident_type", "mode"),
		incidentInfo:    d("incident_info", "1 per open incident (triage, live or paused).", "incident_id", "reference", "name", "status_category", "status", "severity", "incident_type", "mode"),
		incidentCreated: d("incident_created_timestamp_seconds", "Unix creation time of each open incident (triage, live or paused).", "reference", "severity"),

		alertsFiring:      d("alerts_firing", "Firing alerts, by alert source.", "alert_source", "alert_source_type"),
		alertOldestFiring: d("alert_oldest_firing_timestamp_seconds", "Unix creation time of the oldest firing alert, by alert source.", "alert_source", "alert_source_type"),
		heartbeatExpired:  d("heartbeat_expired", "1 if the heartbeat alert source has a firing alert (it missed its pings).", "alert_source"),
		heartbeatInterval: d("heartbeat_interval_seconds", "Expected ping interval of the heartbeat alert source.", "alert_source"),

		escalations:       d("escalations", "Escalations (pages) not resolved, by status, priority and escalation path.", "status", "priority", "escalation_path"),
		escalationCreated: d("escalation_created_timestamp_seconds", "Unix creation time of each triggered or acknowledged escalation.", "escalation_id", "title", "status", "priority", "escalation_path"),

		scheduleInfo:        d("schedule_info", "1 per on-call schedule.", "schedule_id", "schedule_name", "schedule_timezone"),
		scheduleOnCallUsers: d("schedule_oncall_users", "Number of users currently on call per schedule (0 when nobody is).", "schedule_id", "schedule_name"),
		oncallInfo:          d("oncall_info", "1 per user currently on call, per schedule.", "schedule_id", "schedule_name", "schedule_timezone", "user_id", "user_name", "rotation_id"),
		oncallShiftEnd:      d("oncall_shift_end_timestamp_seconds", "Unix end time of each current on-call shift.", "schedule_id", "schedule_name", "user_id", "user_name"),

		maintenanceWindowsActive: d("maintenance_windows_active", "Number of maintenance windows currently active."),
		maintenanceWindowEnd:     d("maintenance_window_end_timestamp_seconds", "Unix end time of each active maintenance window.", "maintenance_window_id", "maintenance_window"),

		alertSourceInfo:          d("alert_source_info", "1 per alert source.", "alert_source_id", "alert_source", "alert_source_type"),
		alertSources:             d("alert_sources", "Alert sources by type.", "alert_source_type"),
		escalationPathInfo:       d("escalation_path_info", "1 per escalation path.", "escalation_path_id", "escalation_path"),
		escalationPathResponders: d("escalation_path_current_responders", "Number of users an escalation path would page right now (when the API returns it).", "escalation_path"),
		teamInfo:                 d("team_info", "1 per team.", "team_id", "team_name"),
		teamMembers:              d("team_members", "Number of members per team.", "team_name"),
		teams:                    d("teams", "Number of teams."),
		severityInfo:             d("severity_info", "1 per incident severity, with its rank.", "severity_id", "severity", "rank"),
		userInfo:                 d("user_info", "1 per active user.", "user_id", "user_name", "base_role"),
		users:                    d("users", "Active users, by base role.", "base_role"),
		followUps:                d("follow_ups", "Follow-ups by status and priority.", "status", "priority"),
	}
}
