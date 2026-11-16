# Monitoring reference

Metrics and alerting for the **standalone Docker daemon**. The Traefik plugin exports no Prometheus metrics; monitor it through Traefik logs ([Traefik provider plugin](traefik-plugin.md)).

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /metrics` | Prometheus exposition format |
| `GET /healthz` | Returns HTTP 200 while the process serves HTTP. It does not test UniFi, Docker, or the store |

Both are served on `METRICS_ADDR` (default `:9090`, all interfaces) with no authentication. Restrict access with a firewall or network policy. See [Configuration](configuration.md#metrics_addr) for the healthcheck limitation with host-qualified addresses.

The registry also exposes the default Go and process collectors.

## What is actually recorded

All metrics are registered at startup, but not all are written by the code on `main`. Verified against `main.go` and `internal/reconciler`:

| Metric | Status |
|---|---|
| `bollard_records_total` | recorded |
| `bollard_records_active` | recorded (after each reconcile tick only) |
| `bollard_record_hostname_conflicts_total` | recorded |
| `bollard_reconcile_orphans_cleaned_total` | recorded |
| `bollard_reconcile_missed_recovered_total` | recorded |
| `bollard_reconcile_iterations_total` | recorded |
| `bollard_reconcile_duration_seconds` | recorded (successful ticks only) |
| `bollard_reconcile_last_timestamp_seconds` | recorded |
| `bollard_docker_events_total` | recorded |
| `bollard_docker_event_errors_total` | recorded (`stage="handle"` only) |
| `bollard_unifi_requests_total` | **declared, never recorded** (no series appear) |
| `bollard_unifi_request_duration_seconds` | **declared, never recorded** |
| `bollard_unifi_api_version` | **declared, never recorded** |
| `bollard_build_info` | recorded |
| `bollard_up` | recorded: `0` during startup, `1` once the event loop starts; never reset to `0` on shutdown |

Do not build alerts on the three UniFi metrics until they are wired. Use `bollard_reconcile_iterations_total`, `bollard_docker_event_errors_total`, and `bollard_records_total{success="false"}` as the available UniFi-failure signals.

## Metrics reference

### DNS records

#### `bollard_records_total`

Counter. Labels: `action` (`created`, `deleted`, `adopted`), `success` (`true`, `false`).

Incremented per lifecycle operation. `success="false"` covers IP resolution failure, UniFi create failure, store write failure after create, store delete failure, and UniFi delete failure. `adopted` is only ever recorded with `success="true"`. A failed create is retried only when a later event or the reconcile missed-event pass sees the container again; a failed delete is not retried (see [Architecture](architecture.md#container-stop)).

```promql
rate(bollard_records_total{action="created"}[5m])
increase(bollard_records_total{success="false"}[15m]) > 0
```

#### `bollard_records_active`

Gauge. Records in the store, refreshed at the end of each reconcile tick. It reads `0` from startup until the first tick (one `RECONCILE_INTERVAL`) even if the store has rows; the log line `state store ready owned_records=N` has the immediate count.

#### `bollard_record_hostname_conflicts_total`

Counter. A start event whose hostname is already owned by a container in the **store**. Conflicts with UniFi records that bollard does not own are not detected.

```promql
increase(bollard_record_hostname_conflicts_total[1h])
```

### Reconcile loop

#### `bollard_reconcile_orphans_cleaned_total`

Counter. Store rows removed because their UniFi record no longer exists (deleted or disabled in UniFi). It does not count stopped containers; bollard does not clean those up on reconcile.

#### `bollard_reconcile_missed_recovered_total`

Counter. Running, enabled, labelled containers with no store row that were registered during reconcile (for example a start event missed during a restart).

#### `bollard_reconcile_iterations_total`

Counter. Label `status` (`success`, `failure`). `failure` means the store or UniFi could not be listed that tick. Per-container recovery failures are logged but do not fail the tick.

```promql
rate(bollard_reconcile_iterations_total{status="failure"}[15m])
```

#### `bollard_reconcile_duration_seconds`

Histogram, buckets 0.1, 0.5, 1, 2, 5, 10, 30 s. Observed for successful ticks only.

```promql
histogram_quantile(0.99, rate(bollard_reconcile_duration_seconds_bucket[15m]))
```

#### `bollard_reconcile_last_timestamp_seconds`

Gauge. Unix time of the last successful tick. Absent until the first tick succeeds.

```promql
time() - bollard_reconcile_last_timestamp_seconds
```

### Docker events

#### `bollard_docker_events_total`

Counter. Label `type` (`start`, `stop`). `stop` covers both `die` and `destroy`, so one `docker rm` of a running container can count twice.

#### `bollard_docker_event_errors_total`

Counter. Label `stage`, currently only `handle`. Increments when handling an event returned an error: UniFi failure, hostname conflict, ambiguous host IP, or store failure. Invalid labels are logged as warnings and are **not** counted.

```promql
rate(bollard_docker_event_errors_total[5m])
```

### UniFi API (declared, not recorded)

`bollard_unifi_requests_total{method,status}`, `bollard_unifi_request_duration_seconds{method}`, and `bollard_unifi_api_version{provider}` are defined but no code writes them on `main`. They will not appear in `/metrics` output.

### Process

#### `bollard_build_info`

Gauge, always `1`. Labels `version`, `goversion`.

#### `bollard_up`

Gauge. `0` while starting, `1` once the event loop begins. It is **not** set to `0` on shutdown or on failure, so `bollard_up == 0` does not indicate an outage. Use the scrape's own `up` series for liveness.

## Alerting

These rules use only metrics that are recorded. Replace `job="bollard"` with your scrape job name. The stuck-reconcile threshold hard-codes the default 300 s interval; use `2 * RECONCILE_INTERVAL` in seconds if you changed it.

```yaml
groups:
  - name: bollard
    interval: 60s
    rules:
      - alert: BollardDown
        expr: up{job="bollard"} == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "bollard is not scrapeable"
          description: "DNS record management may have stopped on {{ $labels.instance }}."

      - alert: BollardReconcileStuck
        expr: (time() - bollard_reconcile_last_timestamp_seconds) > 2 * 300
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard has not reconciled recently"
          description: "No successful reconcile for {{ $value | humanizeDuration }} on {{ $labels.instance }}."

      - alert: BollardReconcileFailing
        expr: rate(bollard_reconcile_iterations_total{status="failure"}[15m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard reconcile is failing"
          description: "The store or UniFi could not be listed. Check logs on {{ $labels.instance }}."

      - alert: BollardDockerEventErrors
        expr: rate(bollard_docker_event_errors_total[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard failed to handle Docker events"
          description: "Some containers may lack DNS records, or deleted containers may leave records in UniFi. Check logs."

      - alert: BollardRecordOperationFailures
        expr: increase(bollard_records_total{success="false"}[15m]) > 0
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "bollard record operation failed"
          description: "A create or delete failed on {{ $labels.instance }}."

      - alert: BollardHostnameConflict
        expr: increase(bollard_record_hostname_conflicts_total[1h]) > 0
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "bollard hostname conflict"
          description: "Two containers claim the same hostname. Review labels."
```

For the Prometheus Operator, put the same `groups` under `spec:` of a `PrometheusRule` (`apiVersion: monitoring.coreos.com/v1`) with the labels your Prometheus selects on.

## Grafana panels

| Panel | Type | Query |
|---|---|---|
| Scrape up | Stat | `up{job="bollard"}` |
| Version | Table | `bollard_build_info` |
| Active records | Stat | `bollard_records_active` |
| Record operations / min | Time series | `rate(bollard_records_total[5m]) * 60` (legend `{{action}} {{success}}`) |
| Reconcile p99 | Time series | `histogram_quantile(0.99, rate(bollard_reconcile_duration_seconds_bucket[15m]))` |
| Reconcile failures / min | Time series | `rate(bollard_reconcile_iterations_total{status="failure"}[15m]) * 60` |
| Event errors / min | Time series | `rate(bollard_docker_event_errors_total[5m]) * 60` |

## Scraping

```yaml
scrape_configs:
  - job_name: bollard
    static_configs:
      - targets: ["bollard:9090"]
```

With `network_mode: host`, use the host's address. A working Compose stack with Prometheus is in [`examples/monitoring-compose.yml`](examples/monitoring-compose.yml).
