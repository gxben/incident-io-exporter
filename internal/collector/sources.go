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
	"fmt"
	"strconv"
	"time"

	incident "github.com/incident-io/sdk-go"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Conservative page sizes: the API doesn't document maximums for every
	// endpoint, and a too large page_size is a 422.
	pageSize         = 50
	schedulePageSize = 25
	// GET /v2/escalation_paths rejects page_size > 25 (422 invalid_range).
	escalationPathPageSize = 25
	// Guards against a pagination cursor that never ends.
	maxPages = 200
)

type alertSourceRef struct {
	name              string
	sourceType        string
	heartbeatInterval int64 // seconds, 0 when not a heartbeat
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func oneOf(values ...string) *map[string][]string {
	return &map[string][]string{"one_of": values}
}

// apiError turns a non-2xx response into an error with a bounded body excerpt.
func apiError(endpoint string, status int, body []byte) error {
	const maxBody = 300
	if len(body) > maxBody {
		body = append(body[:maxBody:maxBody], "…"...)
	}
	return fmt.Errorf("%s: unexpected status %d: %s", endpoint, status, body)
}

// paginate calls fetch with each cursor until the API returns none.
func paginate(fetch func(after *string) (next *string, err error)) error {
	var after *string
	seen := map[string]bool{}
	for range maxPages {
		next, err := fetch(after)
		if err != nil {
			return err
		}
		if next == nil || *next == "" {
			return nil
		}
		if seen[*next] {
			return fmt.Errorf("pagination cursor %q repeated", *next)
		}
		seen[*next] = true
		after = next
	}
	return fmt.Errorf("stopped after %d pages", maxPages)
}

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}

// ─── incidents ────────────────────────────────────────────────────────────────

// Open categories: counted. Of these, the ones still being handled also get a
// per-incident series.
var (
	incidentOpenCategories   = []string{"triage", "live", "paused", "learning"}
	incidentActiveCategories = map[string]bool{"triage": true, "live": true, "paused": true}
)

func (c *Collector) pollIncidents(ctx context.Context) ([]prometheus.Metric, error) {
	type key struct{ category, status, severity, itype, mode string }
	counts := map[key]float64{}
	var out []prometheus.Metric

	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.IncidentsV2ListWithResponse(ctx, &incident.IncidentsV2ListParams{
			PageSize:       new(int64(pageSize)),
			After:          after,
			StatusCategory: oneOf(incidentOpenCategories...),
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("incidents", resp.StatusCode(), resp.Body)
		}
		for _, inc := range resp.JSON200.Incidents {
			severity, itype := "", ""
			if inc.Severity != nil {
				severity = inc.Severity.Name
			}
			if inc.IncidentType != nil {
				itype = inc.IncidentType.Name
			}
			category := string(inc.IncidentStatus.Category)
			k := key{category, inc.IncidentStatus.Name, severity, itype, string(inc.Mode)}
			counts[k]++
			if incidentActiveCategories[category] {
				out = append(out,
					gauge(c.d.incidentInfo, 1, inc.Id, inc.Reference, inc.Name, category, inc.IncidentStatus.Name, severity, itype, string(inc.Mode)),
					gauge(c.d.incidentCreated, float64(inc.CreatedAt.Unix()), inc.Reference, severity),
				)
			}
		}
		if resp.JSON200.PaginationMeta == nil {
			return nil, nil
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	for k, v := range counts {
		out = append(out, gauge(c.d.incidents, v, k.category, k.status, k.severity, k.itype, k.mode))
	}
	return out, nil
}

// ─── alerts and heartbeats ────────────────────────────────────────────────────

func (c *Collector) pollAlerts(ctx context.Context) ([]prometheus.Metric, error) {
	type agg struct {
		count  float64
		oldest time.Time
	}
	bySource := map[string]*agg{}

	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.AlertsV2ListWithResponse(ctx, &incident.AlertsV2ListParams{
			PageSize: pageSize,
			After:    after,
			Status:   oneOf("firing"),
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("alerts", resp.StatusCode(), resp.Body)
		}
		for _, a := range resp.JSON200.Alerts {
			g := bySource[a.AlertSourceId]
			if g == nil {
				g = &agg{oldest: a.CreatedAt}
				bySource[a.AlertSourceId] = g
			}
			g.count++
			if a.CreatedAt.Before(g.oldest) {
				g.oldest = a.CreatedAt
			}
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}

	c.refMu.RLock()
	defer c.refMu.RUnlock()

	var out []prometheus.Metric
	for id, g := range bySource {
		ref, ok := c.alertSources[id]
		if !ok {
			ref = alertSourceRef{name: id}
		}
		out = append(out,
			gauge(c.d.alertsFiring, g.count, ref.name, ref.sourceType),
			gauge(c.d.alertOldestFiring, float64(g.oldest.Unix()), ref.name, ref.sourceType),
		)
	}
	// Heartbeats: every heartbeat source gets a series, 1 while it has a
	// firing alert (the API exposes no heartbeat state of its own).
	for id, ref := range c.alertSources {
		if ref.sourceType != "heartbeat" {
			continue
		}
		expired := 0.0
		if bySource[id] != nil {
			expired = 1
		}
		out = append(out, gauge(c.d.heartbeatExpired, expired, ref.name))
		if ref.heartbeatInterval > 0 {
			out = append(out, gauge(c.d.heartbeatInterval, float64(ref.heartbeatInterval), ref.name))
		}
	}
	return out, nil
}

func (c *Collector) pollAlertSources(ctx context.Context) ([]prometheus.Metric, error) {
	resp, err := c.api.AlertSourcesV2ListWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, apiError("alert_sources", resp.StatusCode(), resp.Body)
	}

	refs := map[string]alertSourceRef{}
	byType := map[string]float64{}
	var out []prometheus.Metric
	for _, s := range resp.JSON200.AlertSources {
		ref := alertSourceRef{name: s.Name, sourceType: string(s.SourceType)}
		if s.HeartbeatOptions != nil {
			ref.heartbeatInterval = s.HeartbeatOptions.IntervalSeconds
		}
		refs[s.Id] = ref
		byType[ref.sourceType]++
		out = append(out, gauge(c.d.alertSourceInfo, 1, s.Id, s.Name, ref.sourceType))
	}
	for t, n := range byType {
		out = append(out, gauge(c.d.alertSources, n, t))
	}

	c.refMu.Lock()
	c.alertSources = refs
	c.refMu.Unlock()
	return out, nil
}

// ─── escalations (pages) ──────────────────────────────────────────────────────

var escalationOpenStatuses = []string{"pending", "triggered", "acked", "snoozed", "delayed", "pending_repeat"}

func (c *Collector) pollEscalations(ctx context.Context) ([]prometheus.Metric, error) {
	type key struct{ status, priority, path string }
	counts := map[key]float64{}
	var out []prometheus.Metric

	c.refMu.RLock()
	paths := c.escalationPaths
	c.refMu.RUnlock()

	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.EscalationsV2ListWithResponse(ctx, &incident.EscalationsV2ListParams{
			PageSize: new(int64(pageSize)),
			After:    after,
			Status:   oneOf(escalationOpenStatuses...),
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("escalations", resp.StatusCode(), resp.Body)
		}
		for _, e := range resp.JSON200.Escalations {
			path := ""
			if e.EscalationPathId != nil {
				path = paths[*e.EscalationPathId]
				if path == "" {
					path = *e.EscalationPathId
				}
			}
			status := string(e.Status)
			counts[key{status, e.Priority.Name, path}]++
			if status == "triggered" || status == "acked" {
				out = append(out, gauge(c.d.escalationCreated, float64(e.CreatedAt.Unix()), e.Id, e.Title, status, e.Priority.Name, path))
			}
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	for k, v := range counts {
		out = append(out, gauge(c.d.escalations, v, k.status, k.priority, k.path))
	}
	return out, nil
}

func (c *Collector) pollEscalationPaths(ctx context.Context) ([]prometheus.Metric, error) {
	names := map[string]string{}
	var out []prometheus.Metric
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.EscalationsV2ListPathsWithResponse(ctx, &incident.EscalationsV2ListPathsParams{
			PageSize: new(int64(escalationPathPageSize)),
			After:    after,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("escalation_paths", resp.StatusCode(), resp.Body)
		}
		for _, p := range resp.JSON200.EscalationPaths {
			names[p.Id] = p.Name
			out = append(out, gauge(c.d.escalationPathInfo, 1, p.Id, p.Name))
			if p.CurrentResponders != nil {
				out = append(out, gauge(c.d.escalationPathResponders, float64(len(*p.CurrentResponders)), p.Name))
			}
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	c.refMu.Lock()
	c.escalationPaths = names
	c.refMu.Unlock()
	return out, nil
}

// ─── on-call ──────────────────────────────────────────────────────────────────

// Schedules carry their current shifts: no per-schedule call is needed.
func (c *Collector) pollOnCall(ctx context.Context) ([]prometheus.Metric, error) {
	var out []prometheus.Metric
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.SchedulesV2ListWithResponse(ctx, &incident.SchedulesV2ListParams{
			PageSize: new(int64(schedulePageSize)),
			After:    after,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("schedules", resp.StatusCode(), resp.Body)
		}
		for _, s := range resp.JSON200.Schedules {
			out = append(out, gauge(c.d.scheduleInfo, 1, s.Id, s.Name, s.Timezone))
			users := map[string]bool{}
			if s.CurrentShifts != nil {
				for _, sh := range *s.CurrentShifts {
					if sh.User == nil {
						continue
					}
					rotation := ""
					if sh.RotationId != nil {
						rotation = *sh.RotationId
					}
					// One series per user and rotation: a user on two
					// rotations of a schedule is on call twice.
					out = append(out,
						gauge(c.d.oncallInfo, 1, s.Id, s.Name, s.Timezone, sh.User.Id, sh.User.Name, rotation),
					)
					if !users[sh.User.Id] {
						out = append(out, gauge(c.d.oncallShiftEnd, float64(sh.EndAt.Unix()), s.Id, s.Name, sh.User.Id, sh.User.Name))
					}
					users[sh.User.Id] = true
				}
			}
			out = append(out, gauge(c.d.scheduleOnCallUsers, float64(len(users)), s.Id, s.Name))
		}
		if resp.JSON200.PaginationMeta == nil {
			return nil, nil
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ─── maintenance windows ──────────────────────────────────────────────────────

func (c *Collector) pollMaintenanceWindows(ctx context.Context) ([]prometheus.Metric, error) {
	var out []prometheus.Metric
	active := 0.0
	status := incident.MaintenanceWindowsV1ListParamsStatus("active")
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.MaintenanceWindowsV1ListWithResponse(ctx, &incident.MaintenanceWindowsV1ListParams{
			PageSize: new(int64(pageSize)),
			After:    after,
			Status:   &status,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("maintenance_windows", resp.StatusCode(), resp.Body)
		}
		for _, w := range resp.JSON200.MaintenanceWindows {
			active++
			out = append(out, gauge(c.d.maintenanceWindowEnd, float64(w.EndAt.Unix()), w.Id, w.Name))
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	return append(out, gauge(c.d.maintenanceWindowsActive, active)), nil
}

// ─── teams, severities, users, follow-ups ─────────────────────────────────────

func (c *Collector) pollTeams(ctx context.Context) ([]prometheus.Metric, error) {
	var out []prometheus.Metric
	teams := 0
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.TeamsV3ListWithResponse(ctx, &incident.TeamsV3ListParams{
			PageSize: new(int64(pageSize)),
			After:    after,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("teams", resp.StatusCode(), resp.Body)
		}
		for _, t := range resp.JSON200.Teams {
			teams++
			out = append(out,
				gauge(c.d.teamInfo, 1, t.Id, t.Name),
				gauge(c.d.teamMembers, float64(len(t.Members)), t.Name),
			)
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	return append(out, gauge(c.d.teams, float64(teams))), nil
}

func (c *Collector) pollSeverities(ctx context.Context) ([]prometheus.Metric, error) {
	resp, err := c.api.SeveritiesV1ListWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if resp.JSON200 == nil {
		return nil, apiError("severities", resp.StatusCode(), resp.Body)
	}
	var out []prometheus.Metric
	for _, s := range resp.JSON200.Severities {
		out = append(out, gauge(c.d.severityInfo, 1, s.Id, s.Name, strconv.FormatInt(s.Rank, 10)))
	}
	return out, nil
}

func (c *Collector) pollUsers(ctx context.Context) ([]prometheus.Metric, error) {
	var out []prometheus.Metric
	byRole := map[string]float64{}
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.UsersV2ListWithResponse(ctx, &incident.UsersV2ListParams{
			PageSize: new(int64(pageSize)),
			After:    after,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("users", resp.StatusCode(), resp.Body)
		}
		for _, u := range resp.JSON200.Users {
			if !u.IsActive {
				continue
			}
			byRole[u.BaseRole.Name]++
			out = append(out, gauge(c.d.userInfo, 1, u.Id, u.Name, u.BaseRole.Name))
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	for r, n := range byRole {
		out = append(out, gauge(c.d.users, n, r))
	}
	return out, nil
}

func (c *Collector) pollFollowUps(ctx context.Context) ([]prometheus.Metric, error) {
	type key struct{ status, priority string }
	counts := map[key]float64{}
	err := paginate(func(after *string) (*string, error) {
		resp, err := c.api.FollowUpsV3ListWithResponse(ctx, &incident.FollowUpsV3ListParams{
			PageSize: new(int64(pageSize)),
			After:    after,
		})
		if err != nil {
			return nil, err
		}
		if resp.JSON200 == nil {
			return nil, apiError("follow_ups", resp.StatusCode(), resp.Body)
		}
		for _, f := range resp.JSON200.FollowUps {
			priority := ""
			if f.Priority != nil {
				priority = f.Priority.Name
			}
			counts[key{string(f.Status), priority}]++
		}
		return resp.JSON200.PaginationMeta.After, nil
	})
	if err != nil {
		return nil, err
	}
	var out []prometheus.Metric
	for k, v := range counts {
		out = append(out, gauge(c.d.followUps, v, k.status, k.priority))
	}
	return out, nil
}
