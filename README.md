# bollard

[![CI](https://github.com/j0sh3rs/bollard/actions/workflows/ci.yml/badge.svg)](https://github.com/j0sh3rs/bollard/actions/workflows/ci.yml)
[![Release](https://github.com/j0sh3rs/bollard/actions/workflows/release-please.yml/badge.svg)](https://github.com/j0sh3rs/bollard/actions/workflows/release-please.yml)

Docker label-driven DNS controller for UniFi Network controllers. Watches Docker container events and creates/deletes A records automatically — no manual static-DNS editing required.

## Table of contents

- [How it works](#how-it-works)
- [Quickstart](#quickstart)
- [Label reference](#label-reference)
- [Environment variables](#environment-variables)
- [UniFi credential setup](#unifi-credential-setup)
- [Recovering after state loss (--adopt)](#recovering-after-state-loss---adopt)
- [Failure modes](#failure-modes)
- [Known limitations](#known-limitations)
- [Documentation](#documentation)

## How it works

bollard subscribes to the Docker event stream. When a container with a `dns.bollard/hostname` label starts, bollard creates a matching A record in your UniFi controller. When the container stops, the record is deleted. A periodic reconcile loop self-heals missed events. Ownership is tracked in a local SQLite database so bollard never modifies records it did not create.

```
Docker event stream
        │
        ▼
  ┌─────────────┐   label parse   ┌──────────────┐
  │   bollard   │ ──────────────► │ state store  │
  │  (watcher)  │                 │  (SQLite)    │
  └─────┬───────┘                 └──────────────┘
        │ create/delete record
        ▼
  ┌─────────────┐
  │  UniFi DNS  │
  │ (A records) │
  └─────────────┘
```

The reconcile loop runs on a configurable interval (`RECONCILE_INTERVAL`, default 5m). It compares running containers against owned records and cleans up any orphans from containers that exited without emitting a stop event.

## Quickstart

Create a `.env` file with your credentials:

```bash
# .env
UNIFI_HOST=https://unifi.home.arpa
UNIFI_API_KEY=your-api-key-here
DATABASE_URL=file:/data/bollard.db
```

Add bollard to your `docker-compose.yml`:

```yaml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    env_file: .env

volumes:
  bollard-data:
```

Label a container to opt it in:

```yaml
services:
  myapp:
    image: myapp:latest
    labels:
      dns.bollard/hostname: myapp.home.arpa
```

bollard will create an A record pointing `myapp.home.arpa` at the NAS host IP when the container starts, and delete it when the container stops.

## Label reference

| Label | Required | Default | Description |
|---|---|---|---|
| `dns.bollard/hostname` | Yes | — | FQDN for the DNS record. Opts the container in. |
| `dns.bollard/record-type` | No | `A` | Record type. A only in current release. |
| `dns.bollard/ttl` | No | `300` | TTL in seconds. |
| `dns.bollard/ip-override` | No | Host IP | Override the inferred NAS host IP. |
| `dns.bollard/enabled` | No | `true` | Set to `false` to suppress DNS management without removing labels. |

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `UNIFI_HOST` | required | UniFi controller URL, e.g. `https://unifi.home.arpa` |
| `UNIFI_API_KEY` | required | API key for a dedicated local UniFi account (see below) |
| `UNIFI_SITE` | `default` | UniFi site name |
| `UNIFI_SKIP_TLS_VERIFY` | `true` | Skip TLS verification. Set to `false` when using `UNIFI_CA_CERT`. |
| `UNIFI_CA_CERT` | — | Path to custom CA certificate PEM file. Overrides skip-verify when set. |
| `DATABASE_URL` | `file:bollard.db` | SQLite DSN. Use an absolute path in production. Postgres URI accepted (post-MVP). |
| `RECONCILE_INTERVAL` | `5m` | How often the reconcile loop runs |
| `LOG_FORMAT` | `logfmt` | `logfmt` or `json` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `METRICS_ADDR` | `:9090` | Address for Prometheus `/metrics` and `/healthz` endpoints |

## UniFi credential setup

> **Security note:** UniFi does not offer a DNS-only role. bollard requires a local account with the **Network Admin** role. Create a dedicated local account (e.g. `bollard`) rather than using your primary admin credential. Do not use a Ubiquiti SSO account.

1. UniFi Network → Settings → Admins & Users → Add local admin
2. Role: Network Admin
3. Generate an API key in the account settings
4. Set `UNIFI_API_KEY` in your `.env` file

## Recovering after state loss (`--adopt`)

If the bollard database is lost but containers are still running:

```yaml
services:
  bollard:
    command: ["--adopt"]
    # rest of config unchanged
```

bollard scans running containers, matches existing UniFi records by hostname + IP, and reclaims ownership. After adopt completes it transitions to normal operation automatically. `--adopt` never deletes records.

## Failure modes

| Failure | Behavior |
|---|---|
| UniFi unreachable at startup | Retries with exponential backoff. Does not crash. |
| UniFi write fails | Logged. Reconcile loop retries on next tick. |
| Container dies without a stop event | Reconcile loop cleans up the orphaned record within one interval. |
| State database unavailable | Fatal — bollard exits. Fix the `DATABASE_URL` and restart. |
| Docker socket unavailable | Fatal — bollard exits. |
| Duplicate hostname across two containers | Second container left unregistered, error logged. |

## Metrics

bollard exposes Prometheus metrics at `http://<host>:<METRICS_ADDR>/metrics` (default `:9090`).

```bash
curl localhost:9090/metrics | grep bollard_up
```

A `/healthz` endpoint returns HTTP 200 for use as a liveness probe:

```bash
curl -sf localhost:9090/healthz && echo "healthy"
```

| Metric | Type | Labels | Description |
|---|---|---|---|
| `bollard_records_total` | Counter | `action` (created/deleted/adopted), `success` (true/false) | DNS record lifecycle operations |
| `bollard_records_active` | Gauge | — | Currently owned DNS records in state store |
| `bollard_record_hostname_conflicts_total` | Counter | — | Duplicate hostname registration attempts |
| `bollard_reconcile_orphans_cleaned_total` | Counter | — | Orphaned state rows cleaned by reconcile |
| `bollard_reconcile_missed_recovered_total` | Counter | — | Missed-event recoveries during reconcile |
| `bollard_reconcile_iterations_total` | Counter | `status` (success/failure) | Reconcile loop runs |
| `bollard_reconcile_duration_seconds` | Histogram | — | Reconcile loop duration |
| `bollard_reconcile_last_timestamp_seconds` | Gauge | — | Unix epoch of last successful reconcile |
| `bollard_docker_events_total` | Counter | `type` (start/stop) | Docker container events received |
| `bollard_docker_event_errors_total` | Counter | `stage` (handle) | Docker event handling errors |
| `bollard_unifi_requests_total` | Counter | `method` (GET/POST/DELETE), `status` (2xx/4xx/5xx/error) | UniFi API calls |
| `bollard_unifi_request_duration_seconds` | Histogram | `method` | UniFi API request latency |
| `bollard_unifi_api_version` | Gauge | `provider` (modern/legacy) | Active UniFi API provider (value=1) |
| `bollard_build_info` | Gauge | `version`, `goversion` | Build metadata (always 1) |
| `bollard_up` | Gauge | — | 1 when running, 0 on shutdown |

For full metric descriptions, PromQL examples, alerting rules, and Grafana panel specs see [docs/monitoring.md](docs/monitoring.md).

## Known limitations

- A records only. CNAME and other types are planned post-MVP.
- Duplicate hostnames across two containers are not supported. The second container is left unregistered with a logged error.
- Record value is the NAS host IP (host networking). Use `dns.bollard/ip-override` for other values.

## Documentation

- [Getting started](docs/getting-started.md) — step-by-step setup for Synology NAS operators
- [Configuration reference](docs/configuration.md) — all env vars and labels documented
- [Operations guide](docs/operations.md) — day-to-day operation, --adopt, backup, upgrade
- [Architecture](docs/architecture.md) — internal design, component diagram, event flow
- [Examples](docs/examples/) — ready-to-use compose files for common setups
