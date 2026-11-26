# incident-io-exporter

A [Prometheus](https://prometheus.io/) exporter for [incident.io](https://incident.io/). Exposes incidents, firing alerts, heartbeats, escalations (pages), on-call schedules, maintenance windows, follow-ups, teams and users as Prometheus metrics.

Built on the official [incident.io Go SDK](https://github.com/incident-io/sdk-go) ([docs](https://docs.incident.io/integrations/sdk-go)).

---

## How it works

The exporter polls the incident.io API **in the background** and serves the last good snapshot. A scrape never calls the API, so it answers instantly whatever the scrape interval, and the API load doesn't depend on how many Prometheus servers (or replicas) scrape it.

- **Two cadences.** What changes by the minute (incidents, alerts, escalations, on-call, maintenance windows) is polled every `--incidentio.poll-interval` (60s); reference data (users, teams, severities, alert sources, escalation paths, follow-ups) every `--incidentio.slow-poll-interval` (10m).
- **Independent sources.** Each source succeeds or fails on its own. A failing source keeps serving its previous values; `incidentio_source_last_success_timestamp_seconds` says how old they are.
- **Rate limits.** incident.io allows 1200 requests per minute per API key. The exporter spends a client-side budget (token bucket, 2 req/s by default, `--incidentio.rate-limit`) shared by every request, retries included, and retries network errors, `429` and `5xx` with exponential backoff, honouring `Retry-After`. Sources are polled sequentially, so they queue on that budget instead of bursting. Every request is counted per endpoint and status code, `429`s separately.

---

## Metrics

### Incidents

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_incidents` | Gauge | `status_category`, `status`, `severity`, `incident_type`, `mode` | Incidents not closed (status category `triage`, `live`, `paused`, `learning`) |
| `incidentio_incident_info` | Gauge | `incident_id`, `reference`, `name`, `status_category`, `status`, `severity`, `incident_type`, `mode` | 1 per incident still being handled (`triage`, `live`, `paused`) |
| `incidentio_incident_created_timestamp_seconds` | Gauge | `reference`, `severity` | Creation time of each incident still being handled |

### Alerts and heartbeats

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_alerts_firing` | Gauge | `alert_source`, `alert_source_type` | Firing alerts per alert source |
| `incidentio_alert_oldest_firing_timestamp_seconds` | Gauge | `alert_source`, `alert_source_type` | Creation time of the oldest firing alert per source |
| `incidentio_heartbeat_expired` | Gauge | `alert_source` | 1 while the heartbeat alert source has a firing alert (it missed its pings); one series per heartbeat source |
| `incidentio_heartbeat_interval_seconds` | Gauge | `alert_source` | Expected ping interval of the heartbeat |

The API has no heartbeat state of its own: a heartbeat is an alert source of type `heartbeat`, and a missed ping is a firing alert from it.

### Escalations (pages)

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_escalations` | Gauge | `status`, `priority`, `escalation_path` | Escalations not resolved (`pending`, `triggered`, `acked`, `snoozed`, `delayed`, `pending_repeat`) |
| `incidentio_escalation_created_timestamp_seconds` | Gauge | `escalation_id`, `title`, `status`, `priority`, `escalation_path` | Creation time of each `triggered` or `acked` escalation |

### On-call

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_schedule_info` | Gauge | `schedule_id`, `schedule_name`, `schedule_timezone` | 1 per schedule |
| `incidentio_schedule_oncall_users` | Gauge | `schedule_id`, `schedule_name` | Users currently on call per schedule — **0 when nobody is** |
| `incidentio_oncall_info` | Gauge | `schedule_id`, `schedule_name`, `schedule_timezone`, `user_id`, `user_name`, `rotation_id` | 1 per user currently on call, per schedule and rotation |
| `incidentio_oncall_shift_end_timestamp_seconds` | Gauge | `schedule_id`, `schedule_name`, `user_id`, `user_name` | End of each current shift |

### Maintenance windows

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_maintenance_windows_active` | Gauge | — | Maintenance windows currently active |
| `incidentio_maintenance_window_end_timestamp_seconds` | Gauge | `maintenance_window_id`, `maintenance_window` | End of each active window |

### Reference data

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_alert_source_info` | Gauge | `alert_source_id`, `alert_source`, `alert_source_type` | 1 per alert source |
| `incidentio_alert_sources` | Gauge | `alert_source_type` | Alert sources per type |
| `incidentio_escalation_path_info` | Gauge | `escalation_path_id`, `escalation_path` | 1 per escalation path |
| `incidentio_escalation_path_current_responders` | Gauge | `escalation_path` | Users the path would page right now (when the API returns it) |
| `incidentio_team_info` | Gauge | `team_id`, `team_name` | 1 per team |
| `incidentio_team_members` | Gauge | `team_name` | Members per team |
| `incidentio_teams` | Gauge | — | Number of teams |
| `incidentio_severity_info` | Gauge | `severity_id`, `severity`, `rank` | 1 per severity |
| `incidentio_user_info` | Gauge | `user_id`, `user_name`, `base_role` | 1 per active user |
| `incidentio_users` | Gauge | `base_role` | Active users per base role |
| `incidentio_follow_ups` | Gauge | `status`, `priority` | Follow-ups per status and priority |

### Exporter

| Metric | Type | Labels | Description |
|---|---|---|---|
| `incidentio_up` | Gauge | — | 1 if the last poll of every enabled source succeeded |
| `incidentio_source_up` | Gauge | `source` | 1 if the last poll of the source succeeded |
| `incidentio_source_last_success_timestamp_seconds` | Gauge | `source` | Last successful poll: the age of the data served |
| `incidentio_source_poll_duration_seconds` | Gauge | `source` | Duration of the last poll (pagination and retries included) |
| `incidentio_source_poll_errors_total` | Counter | `source` | Failed polls since start |
| `incidentio_api_requests_total` | Counter | `endpoint`, `code` | API requests, each retry counted (`code="error"` for transport failures) |
| `incidentio_api_rate_limited_total` | Counter | `endpoint` | `429` answers |
| `incidentio_api_request_duration_seconds` | Histogram | `endpoint` | API request duration |
| `incidentio_api_throttle_wait_seconds` | Histogram | — | Time spent waiting on the client-side rate limiter |
| `incidentio_exporter_build_info` | Gauge | `version`, `goversion` | Build information |

Plus the standard `go_*` and `process_*` metrics.

### Useful queries

```promql
# Who is on call right now?
incidentio_oncall_info

# Schedules with nobody on call
incidentio_schedule_oncall_users == 0

# Missed heartbeats
incidentio_heartbeat_expired == 1

# Pages triggered for more than 15 minutes without acknowledgement
time() - incidentio_escalation_created_timestamp_seconds{status="triggered"} > 900

# Live incidents by severity
sum by (severity) (incidentio_incidents{status_category="live"})

# Data older than 15 minutes
time() - incidentio_source_last_success_timestamp_seconds > 900
```

### Coming from opsgenie-exporter

| opsgenie-exporter | incident-io-exporter |
|---|---|
| `opsgenie_oncall_info` | `incidentio_oncall_info` (+ `incidentio_schedule_oncall_users`, which is 0 rather than absent when nobody is on call) |
| `opsgenie_alerts_total{status,priority}` | `incidentio_alerts_firing{alert_source}` (incident.io alerts have no priority; pages do: `incidentio_escalations{priority}`) |
| `opsgenie_heartbeat_expired` | `incidentio_heartbeat_expired` |
| `opsgenie_incidents_total` / `opsgenie_incident_info` | `incidentio_incidents` / `incidentio_incident_info` |
| `opsgenie_team_info` / `opsgenie_teams_total` | `incidentio_team_info` / `incidentio_teams` |
| `opsgenie_user_info` / `opsgenie_users_total` | `incidentio_user_info` / `incidentio_users` |
| `opsgenie_scrape_success` | `incidentio_up`, `incidentio_source_up` |

---

## Requirements

- An incident.io API key (Settings → API keys). The exporter only lists resources: grant the "view" permissions only.
- Go 1.26+ (to build from source)

---

## Usage

### Binary

```bash
# API key via environment variable (recommended)
export INCIDENT_IO_API_KEY=<YOUR_KEY>
incident-io-exporter

# Skip sources you don't need (saves API calls)
incident-io-exporter --incidentio.disable-sources=users,follow_ups
```

All flags:

| Flag | Default | Description |
|---|---|---|
| `--incidentio.api-key` | — | API key (`INCIDENT_IO_API_KEY` env var as fallback) |
| `--incidentio.api-url` | `https://api.incident.io` | API base URL |
| `--incidentio.poll-interval` | `60s` | Poll interval of incidents, alerts, escalations, on-call, maintenance windows |
| `--incidentio.slow-poll-interval` | `10m` | Poll interval of users, teams, severities, alert sources, escalation paths, follow-ups |
| `--incidentio.poll-timeout` | `2m` | Timeout of one source's poll, pagination and retries included |
| `--incidentio.rate-limit` | `2` | Client-side budget in requests per second (`0` disables it) |
| `--incidentio.rate-burst` | `5` | Burst size of the client-side limiter |
| `--incidentio.max-retries` | `4` | Retries on network errors, `429` and `5xx` (`Retry-After` honoured) |
| `--incidentio.request-timeout` | `30s` | Timeout of a single HTTP request |
| `--incidentio.disable-sources` | — | Comma-separated sources not to poll: `alert_sources`, `escalation_paths`, `teams`, `severities`, `users`, `follow_ups`, `incidents`, `alerts`, `escalations`, `oncall`, `maintenance_windows` |
| `--log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `--web.listen-address` | `:9429` | Address to expose metrics on |
| `--web.telemetry-path` | `/metrics` | Path to expose metrics at |
| `--version` | — | Print version and exit |

Disabling `alert_sources` leaves alerts labelled by source id and drops the heartbeat metrics; disabling `escalation_paths` leaves escalations labelled by path id.

Endpoints exposed:

| Path | Description |
|---|---|
| `/metrics` | Prometheus metrics (OpenMetrics format) |
| `/healthz` | Liveness probe — returns `200 ok` |
| `/` | Landing page with link to `/metrics` |

### Docker

```bash
docker run --rm \
  -e INCIDENT_IO_API_KEY=<YOUR_KEY> \
  -p 9429:9429 \
  ghcr.io/gxben/incident-io-exporter:latest
```

### Helm (Kubernetes)

The chart is published to GHCR as an OCI artifact.

```bash
# Install with a chart-managed Secret
helm install incident-io-exporter \
  oci://ghcr.io/gxben/charts/incident-io-exporter \
  --set incidentio.apiKey=<YOUR_KEY>

# Use a pre-existing Secret (e.g. managed by External Secrets Operator)
helm install incident-io-exporter \
  oci://ghcr.io/gxben/charts/incident-io-exporter \
  --set incidentio.existingSecret=my-secret \
  --set incidentio.existingSecretKey=api-key
```

Enable Prometheus Operator integration:

```bash
helm install incident-io-exporter \
  oci://ghcr.io/gxben/charts/incident-io-exporter \
  --set incidentio.apiKey=<YOUR_KEY> \
  --set serviceMonitor.enabled=true \
  --set prometheusRule.enabled=true
```

The chart ships four default alert rules when `prometheusRule.enabled=true`:

| Alert | Severity | Condition |
|---|---|---|
| `IncidentIOExporterStale` | warning | A source hasn't been polled successfully for 15 min |
| `IncidentIOHeartbeatExpired` | critical | `incidentio_heartbeat_expired == 1` for 5 min |
| `IncidentIOEscalationNotAcknowledged` | warning | A page triggered for more than 15 min without acknowledgement |
| `IncidentIONoOnCallUser` | critical | A schedule has nobody on call for 5 min |

Key chart values:

| Value | Default | Description |
|---|---|---|
| `incidentio.apiKey` | `""` | API key (stored in a chart-managed Secret) |
| `incidentio.existingSecret` | `""` | Use a pre-existing Secret instead |
| `incidentio.existingSecretKey` | `"api-key"` | Key of the API key in that Secret |
| `incidentio.apiURL` | `""` | API base URL; empty = `https://api.incident.io` |
| `incidentio.pollInterval` / `slowPollInterval` | `"60s"` / `"10m"` | Poll cadences |
| `incidentio.rateLimit` / `rateBurst` | `2` / `5` | Client-side API budget |
| `incidentio.maxRetries` | `4` | Retries on network errors, `429` and `5xx` |
| `incidentio.disableSources` | `[]` | Sources not to poll |
| `serviceMonitor.enabled` | `false` | Create a Prometheus Operator `ServiceMonitor` |
| `serviceMonitor.interval` / `scrapeTimeout` | `"60s"` / `"10s"` | Scrapes read the cached snapshot: short is fine |
| `prometheusRule.enabled` | `false` | Create a Prometheus Operator `PrometheusRule` |

---

## Development

### Pre-commit hooks

```bash
pip install pre-commit
pre-commit install --hook-type pre-commit --hook-type commit-msg
```

Hooks enforce Conventional Commits format and run `go build`, `go mod tidy`, `go test`, `go fmt`, `golangci-lint`, `gosec`, and `govulncheck` before every push.

### Commit convention

This project uses [Conventional Commits](https://www.conventionalcommits.org/):

| Type | Release |
|---|---|
| `feat` | minor |
| `fix`, `perf`, `refactor`, `revert` | patch |
| `BREAKING CHANGE` footer or `!` suffix | major |
| `docs`, `chore`, `style`, `test`, `ci` | none |

### Make targets

Tools (`golangci-lint`, `govulncheck`, `gosec`) are downloaded on demand into `bin/` — no global installation required.

```bash
make build        # compile to bin/incident-io-exporter
make run          # build and run (reads INCIDENT_IO_API_KEY from env)
make test         # run tests with race detector
make fmt / vet / lint / sec / vuln
make docker       # build multi-arch image locally (no push)
make docker-push  # build and push to GHCR
make helm-package # package the Helm chart as a .tgz
make clean        # remove bin/, coverage.out, *.tgz
```

### CI/CD

| Workflow | Trigger | Purpose |
|---|---|---|
| `ci.yml` | push / PR to `main` | Tests, lint, build, Docker image (push on `main` only) |
| `sec.yml` | push / PR to `main` | gosec security scan |
| `vuln.yml` | push / PR to `main` | govulncheck CVE scan |
| `release.yml` | manual `workflow_dispatch` | semantic-release → Docker image → Helm chart → ArtifactHub |

Trigger a release by running the `release.yml` workflow manually. It determines the next version from conventional commits, updates `CHANGELOG.md`, creates a GitHub Release, and publishes the Docker image and Helm chart.

> **Note**: set `RELEASE_TOKEN` in repository secrets (PAT with `contents: write`) so semantic-release can push the CHANGELOG commit back to `main` when branch protection is enabled.

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
Copyright 2026 Benjamin Zores.
