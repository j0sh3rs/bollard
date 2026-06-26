# Monitoring reference

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /metrics` | Prometheus text exposition format |
| `GET /healthz` | Liveness probe — returns HTTP 200 when bollard is running |

Both endpoints are served on the address configured by `METRICS_ADDR` (default `:9090`).

```bash
# verify metrics
curl -s localhost:9090/metrics | grep bollard_up

# liveness check
curl -sf localhost:9090/healthz && echo "healthy"
```

**Security:** neither endpoint has authentication. In production bind `METRICS_ADDR` to a loopback address or restrict access via network policy / firewall rules.

```bash
# loopback-only
METRICS_ADDR=127.0.0.1:9090
```

---

## Metrics reference

### DNS Records

#### `bollard_records_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | `action` (`created`, `deleted`, `adopted`), `success` (`true`, `false`) |

Incremented on every DNS record lifecycle operation. A `success=false` sample indicates the UniFi write failed; the reconcile loop will retry.

```promql
# creation rate over 5 minutes
rate(bollard_records_total{action="created"}[5m])

# failure rate (any action)
rate(bollard_records_total{success="false"}[5m])
```

---

#### `bollard_records_active`

| Field | Value |
|---|---|
| Type | Gauge |
| Labels | none |

Number of DNS records currently owned by bollard in the state store. Drops to zero only if all owned records are deleted or the store is wiped.

```promql
bollard_records_active
```

---

#### `bollard_record_hostname_conflicts_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | none |

Incremented when a second container attempts to register a hostname already owned by another container. The conflicting container is left unregistered.

```promql
increase(bollard_record_hostname_conflicts_total[1h])
```

---

### Reconcile Loop

#### `bollard_reconcile_orphans_cleaned_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | none |

Orphaned state-store rows cleaned during reconcile — records in the store whose container is no longer running. Triggered when a container exits without emitting a Docker stop event.

```promql
increase(bollard_reconcile_orphans_cleaned_total[1h])
```

---

#### `bollard_reconcile_missed_recovered_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | none |

DNS records re-created during reconcile for containers that are running but have no matching UniFi record (missed-event recovery).

```promql
increase(bollard_reconcile_missed_recovered_total[1h])
```

---

#### `bollard_reconcile_iterations_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | `status` (`success`, `failure`) |

Total reconcile loop iterations. A `failure` sample means the loop encountered an unrecoverable error during that iteration.

```promql
# success rate
rate(bollard_reconcile_iterations_total{status="success"}[15m])

# failure rate
rate(bollard_reconcile_iterations_total{status="failure"}[15m])
```

---

#### `bollard_reconcile_duration_seconds`

| Field | Value |
|---|---|
| Type | Histogram |
| Labels | none |
| Buckets | 0.1, 0.5, 1, 2, 5, 10, 30 seconds |

Duration of each reconcile loop iteration.

```promql
# 99th-percentile reconcile duration
histogram_quantile(0.99, rate(bollard_reconcile_duration_seconds_bucket[15m]))
```

---

#### `bollard_reconcile_last_timestamp_seconds`

| Field | Value |
|---|---|
| Type | Gauge |
| Labels | none |

Unix epoch timestamp of the last successfully completed reconcile. Use to detect a stuck reconcile loop.

```promql
# seconds since last successful reconcile
time() - bollard_reconcile_last_timestamp_seconds
```

---

### Docker Events

#### `bollard_docker_events_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | `type` (`start`, `stop`) |

Docker container events received from the event stream.

```promql
rate(bollard_docker_events_total[5m])
```

---

#### `bollard_docker_event_errors_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | `stage` (`handle`) |

Errors encountered while processing Docker events. Increment indicates a label parse error or a failed UniFi write triggered by an event.

```promql
rate(bollard_docker_event_errors_total[5m])
```

---

### UniFi API

#### `bollard_unifi_requests_total`

| Field | Value |
|---|---|
| Type | Counter |
| Labels | `method` (`GET`, `POST`, `DELETE`), `status` (`2xx`, `4xx`, `5xx`, `error`) |

HTTP requests made to the UniFi API. `error` status indicates a network-level failure (no HTTP response received).

```promql
# error rate
rate(bollard_unifi_requests_total{status=~"5xx|error"}[5m])

# request rate by method
rate(bollard_unifi_requests_total[5m])
```

---

#### `bollard_unifi_request_duration_seconds`

| Field | Value |
|---|---|
| Type | Histogram |
| Labels | `method` (`GET`, `POST`, `DELETE`) |
| Buckets | 0.01, 0.05, 0.1, 0.5, 1, 2, 5 seconds |

Latency of UniFi API requests.

```promql
# 95th-percentile latency for POST requests
histogram_quantile(0.95, rate(bollard_unifi_request_duration_seconds_bucket{method="POST"}[5m]))
```

---

#### `bollard_unifi_api_version`

| Field | Value |
|---|---|
| Type | Gauge |
| Labels | `provider` (`modern`, `legacy`) |

Indicates which UniFi API provider bollard negotiated at startup. Set to `1` for the active provider, absent for the inactive one. `modern` = UniFi Network Application ≥7.x REST API; `legacy` = older API path.

```promql
bollard_unifi_api_version
```

---

### Process

#### `bollard_build_info`

| Field | Value |
|---|---|
| Type | Gauge |
| Labels | `version` (semver), `goversion` (e.g. `go1.22.3`) |

Always `1`. Use to correlate dashboards with deployed version and join on `version` in alerting rules.

```promql
bollard_build_info
```

---

#### `bollard_up`

| Field | Value |
|---|---|
| Type | Gauge |
| Labels | none |

`1` when bollard is running normally. Set to `0` on graceful shutdown before the process exits. Absence of this metric (scrape fails) also indicates bollard is down.

```promql
bollard_up
```

---

## Alerting

### PrometheusRule (Kubernetes CRD)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: bollard
  namespace: monitoring
  labels:
    prometheus: kube-prometheus
    role: alert-rules
spec:
  groups:
    - name: bollard
      interval: 60s
      rules:
        - alert: BollardDown
          expr: bollard_up == 0
          for: 1m
          labels:
            severity: critical
          annotations:
            summary: "bollard is down"
            description: "bollard_up is 0 on {{ $labels.instance }}. DNS record management has stopped."

        - alert: BollardReconcileStuck
          expr: (time() - bollard_reconcile_last_timestamp_seconds) > 2 * 300
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "bollard reconcile loop is stuck"
            description: >
              Last reconcile was {{ $value | humanizeDuration }} ago on {{ $labels.instance }}.
              Expected every RECONCILE_INTERVAL (default 5m). Orphaned records may not be cleaned.

        - alert: BollardReconcileFailing
          expr: rate(bollard_reconcile_iterations_total{status="failure"}[15m]) > 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "bollard reconcile loop is failing"
            description: >
              Reconcile failures detected on {{ $labels.instance }}.
              Check logs for root cause. DNS records may become stale.

        - alert: BollardUnifiAPIErrors
          expr: rate(bollard_unifi_requests_total{status=~"5xx|error"}[5m]) > 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "bollard UniFi API errors"
            description: >
              UniFi API requests are returning 5xx or network errors on {{ $labels.instance }}.
              DNS record operations may be failing silently.

        - alert: BollardDockerEventErrors
          expr: rate(bollard_docker_event_errors_total[5m]) > 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "bollard Docker event handling errors"
            description: >
              Errors processing Docker events on {{ $labels.instance }}.
              Some container start/stop events may have been dropped.

        - alert: BollardHostnameConflict
          expr: increase(bollard_record_hostname_conflicts_total[1h]) > 0
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "bollard hostname conflict detected"
            description: >
              A container attempted to register a hostname already owned by another container on
              {{ $labels.instance }}. The conflicting container has no DNS record.
              Review container labels for duplicate hostnames.
```

### Standalone `alerts.yml` (bare Prometheus)

```yaml
groups:
  - name: bollard
    interval: 60s
    rules:
      - alert: BollardDown
        expr: bollard_up == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "bollard is down"
          description: "bollard_up is 0. DNS record management has stopped."

      - alert: BollardReconcileStuck
        expr: (time() - bollard_reconcile_last_timestamp_seconds) > 2 * 300
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard reconcile loop is stuck"
          description: >
            Last reconcile was {{ $value | humanizeDuration }} ago.
            Expected every RECONCILE_INTERVAL (default 5m).

      - alert: BollardReconcileFailing
        expr: rate(bollard_reconcile_iterations_total{status="failure"}[15m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard reconcile loop is failing"
          description: "Reconcile failures detected. Check logs."

      - alert: BollardUnifiAPIErrors
        expr: rate(bollard_unifi_requests_total{status=~"5xx|error"}[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard UniFi API errors"
          description: "UniFi API requests are returning 5xx or network errors."

      - alert: BollardDockerEventErrors
        expr: rate(bollard_docker_event_errors_total[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "bollard Docker event handling errors"
          description: "Errors processing Docker events. Some events may have been dropped."

      - alert: BollardHostnameConflict
        expr: increase(bollard_record_hostname_conflicts_total[1h]) > 0
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "bollard hostname conflict detected"
          description: "A container attempted to register a duplicate hostname. Review container labels."
```

> **Threshold for `BollardReconcileStuck`:** the expression uses `300` (seconds) as the default `RECONCILE_INTERVAL`. If you change `RECONCILE_INTERVAL`, update the threshold to `2 * <interval_in_seconds>`.

---

## Grafana dashboard panels

Build a dashboard from these five panels. Import the datasource UID for your Prometheus instance.

### Panel 1 — Process health

| Field | Value |
|---|---|
| Title | `bollard health` |
| Visualization | Stat |
| PromQL | `bollard_up` |

Set value mappings: `1` → `Running` (green), `0` → `Down` (red). Add `bollard_build_info` as a table panel below to display the deployed version.

### Panel 2 — Active DNS records

| Field | Value |
|---|---|
| Title | `Active DNS records` |
| Visualization | Stat |
| PromQL | `bollard_records_active` |

### Panel 3 — Record operations rate

| Field | Value |
|---|---|
| Title | `Record operations / min` |
| Visualization | Time series |
| PromQL | `rate(bollard_records_total[5m]) * 60` |

Legend: `{{action}} {{success}}`. Helps identify creation/deletion bursts.

### Panel 4 — Reconcile duration

| Field | Value |
|---|---|
| Title | `Reconcile p99 duration` |
| Visualization | Time series |
| PromQL | `histogram_quantile(0.99, rate(bollard_reconcile_duration_seconds_bucket[15m]))` |

Add a second series for p50: `histogram_quantile(0.50, rate(bollard_reconcile_duration_seconds_bucket[15m]))`.

### Panel 5 — UniFi API error rate

| Field | Value |
|---|---|
| Title | `UniFi API errors / min` |
| Visualization | Time series |
| PromQL | `rate(bollard_unifi_requests_total{status=~"5xx|error"}[5m]) * 60` |

Non-zero values indicate connectivity problems with the UniFi controller.

---

## Docker Compose integration

### Adding bollard to an existing Prometheus stack

Add a scrape job to your Prometheus `scrape_configs`:

```yaml
scrape_configs:
  - job_name: bollard
    static_configs:
      - targets: ["bollard:9090"]
```

If bollard uses `network_mode: host`, replace `bollard` with the host IP or DNS name reachable from the Prometheus container.

### Minimal compose example

See [`docs/examples/monitoring-compose.yml`](examples/monitoring-compose.yml) for a working bollard + Prometheus compose stack.
