# Monitoring reference

Metrics and alerting for the **standalone Docker daemon**. The Traefik plugin exports no Prometheus metrics; monitor it through Traefik logs ([Traefik provider plugin](traefik-plugin.md)).

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /metrics` | Prometheus exposition format |
| `GET /healthz` | Returns HTTP 200 while the process serves HTTP. It does not test UniFi, Docker, or the store |

Both endpoints are served on `METRICS_ADDR` (default `:9090`, all interfaces) with no authentication. Bind to loopback or restrict access with a firewall or network policy. See [host-qualified examples](configuration.md#metrics_addr).

The registry also exposes the default Go and process collectors.

## Recorded metrics

All metrics are registered at startup. Labelled series appear when first observed.

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
| `bollard_unifi_requests_total` | every HTTP attempt, including retries |
| `bollard_unifi_request_duration_seconds` | duration of each HTTP attempt |
| `bollard_unifi_api_version` | set when endpoint selection succeeds |
| `bollard_build_info` | recorded |
| `bollard_up` | `0` during startup, `1` in the event loop, reset to `0` on shutdown |

## Metrics reference

### DNS records

#### `bollard_records_total`

Counter. Labels: `action` (`created`, `deleted`, `adopted`), `success` (`true`, `false`).

Incremented per lifecycle operation. `success=\"false\"` records failed operations such as IP resolution, UniFi calls, or a store write after create. `adopted` is recorded only with `success=\"true\"`. Failed deletes with an ownership row are retried during reconcile.

```promql
rate(bollard_records_total{action="created"}[5m])
increase(bollard_records_total{success="false"}[15m]) > 0
```

#### `bollard_records_active`

Gauge. Records in the store, refreshed at the end of each reconcile tick. It reads `0` from startup until the first tick (one `RECONCILE_INTERVAL`) even if the store has rows; the log line `state store ready owned_records=N` has the immediate count.

#### `bollard_record_hostname_conflicts_total`

Counter. A hostname conflict detected during record creation or adoption. Existing names in the store or UniFi block creation.

```promql
increase(bollard_record_hostname_conflicts_total[1h])
```

### Reconcile loop

#### `bollard_reconcile_orphans_cleaned_total`

Counter. Store rows removed because their UniFi record no longer exists. Stopped-container deletions are counted under `bollard_records_total`, not orphan cleanup.

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

### UniFi API

- `bollard_unifi_requests_total{method,status}` counts each attempt, including startup probes and retries. `status` is the HTTP code or `error` if no response arrived.
- `bollard_unifi_request_duration_seconds{method}` times each attempt, excluding backoff between attempts.
- `bollard_unifi_api_version{provider}` is `1` for the selected `modern` or `legacy` provider.

### Process

#### `bollard_build_info`

Gauge, always `1`. Labels `version`, `goversion`.

#### `bollard_up`

Gauge. `0` during startup, including UniFi retries, `1` once the event loop begins, and reset to `0` on shutdown. The endpoint disappears when the process exits; use the scrape's own `up` series to detect that.

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
