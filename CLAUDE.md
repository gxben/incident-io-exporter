# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build        # build binary to bin/incident-io-exporter
make run          # build + run (requires INCIDENT_IO_API_KEY env var)
make test         # go test ./... -race -count=1
make lint         # golangci-lint (downloads to bin/ if absent)
make fmt          # go fmt
make vet          # go vet
make sec          # gosec (downloads to bin/ if absent)
make vuln         # govulncheck (downloads to bin/ if absent)
make docker       # multi-arch buildx (no push)
make docker-push  # build + push to GHCR
make helm-package # package charts/incident-io-exporter
```

Run a single test:
```bash
go test ./internal/collector/... -run TestCollectorSnapshot -v
```

## Architecture

**Entry point** (`main.go`): parses flags, builds the API client (`internal/api`), registers the collector and the client's request metrics on a fresh Prometheus registry, starts the background poller (`coll.Run`) and serves `/metrics` + `/healthz`. SIGINT/SIGTERM stop the poller and shut the server down.

**API client** (`internal/api/client.go`): the incident.io SDK (`github.com/incident-io/sdk-go`, generated from the OpenAPI spec) with our own HTTP stack passed through `incident.WithHTTPClient`:

```
retryablehttp (exponential backoff; Retry-After honoured on 429/503)
  -> limitedTransport (token bucket, then per-endpoint metrics)
    -> http.DefaultTransport
```

The limiter sits below the retry loop on purpose: retries spend the same budget as first attempts. Don't also pass the SDK's `WithRetries()`: it installs its own HTTP client and the later option wins.

**Collector** (`internal/collector/`): polls in the background, scrapes only read the snapshot.
- `collector.go`: sources, the fast/slow tickers, per-source state (last good metrics, success, duration, last success time, error count) and `Collect()`.
- `sources.go`: one `poll*` function per source, each returning `[]prometheus.Metric` built with `MustNewConstMetric`. A failed poll keeps the previous metrics.
- `descs.go`: every metric descriptor.
- Slow sources run before fast ones at start: `alert_sources` and `escalation_paths` fill the name maps the `alerts` and `escalations` sources use (`refMu`).
- Sources run sequentially: they share one rate-limit budget, parallelism would only queue on it.
- The collector is unchecked (`Describe` sends nothing): label values come from the API.

### incident.io API notes

- List filters are deep-object maps: `oneOf("firing")` → `status[one_of]=firing`. The SDK rewrites array filters so repeated values work.
- Pagination: `pagination_meta.after` until empty; `paginate()` also stops on a repeated cursor or after `maxPages`.
- Page sizes aren't documented for every endpoint; a too large `page_size` is a 422. Keep `pageSize` (50), `schedulePageSize` (25, the largest that still returns `next_shifts`) and `escalationPathPageSize` (25: `/v2/escalation_paths` rejects more with a 422).
- `SchedulesV2List` returns each schedule's `current_shifts`: on-call needs no per-schedule call.
- There is no heartbeat endpoint: heartbeats are alert sources of type `heartbeat`; a missed ping is a firing alert from that source.
- Some endpoints have several versions; the SDK marks deprecated ones `// Deprecated:` (e.g. `FollowUpsV2List` → use `FollowUpsV3List`). The SDK ships API changes as minor versions: check the API changelog when bumping it.
- Rate limit: 1200 requests/minute per API key, shared with every other integration using the key.

### CI / release pipeline

- **ci.yml**: test → lint → build → docker (push on `main`, not on PRs)
- **release.yml**: manual trigger → semantic-release (CHANGELOG + git tag) → docker (versioned tags + `latest`) → Helm OCI push to `ghcr.io/<owner>/charts` → ArtifactHub metadata push
- **sec.yml** / **vuln.yml**: gosec and govulncheck on push/PR
- Commits must follow Conventional Commits (enforced by pre-commit hook + semantic-release)
- `RELEASE_TOKEN` secret required for semantic-release to push back to `main` through branch protection; falls back to `GITHUB_TOKEN`

### Helm chart

`charts/incident-io-exporter/` mirrors the deployment exactly. Key values:
- `incidentio.apiKey` / `incidentio.existingSecret` — mutually exclusive; secret is checksum-annotated on the deployment
- `incidentio.pollInterval`, `slowPollInterval`, `rateLimit`, `rateBurst`, `maxRetries`, `disableSources` map to the flags
- `serviceMonitor.enabled` / `prometheusRule.enabled` — off by default; require Prometheus Operator CRDs
