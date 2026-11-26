# Architecture

This page explains how the **standalone Docker daemon** works and why it is built that way. It describes the code on `main`. For the Traefik provider plugin (a separate, stateless runtime that shares the UniFi client but not the daemon's store or reconciler) see [Traefik provider plugin](traefik-plugin.md).

| | Standalone daemon (this page) | Traefik plugin |
|---|---|---|
| Runs as | its own container | inside Traefik |
| Discovers hosts from | Docker events + `dns.bollard/*` labels | Traefik router `Host()` rules |
| Needs Docker socket | yes (read-only) | no |
| State | SQLite or Postgres ownership store | none |
| Deletes records | yes, records it owns | only records it created, at shutdown |

## Component diagram

```
Docker socket ──► Watcher ──► Reconciler ◄── ticker (RECONCILE_INTERVAL)
                                  │
                   ┌──────────────┼───────────────┐
                   ▼              ▼               ▼
             Label parser     State store     UniFi provider ──► UniFi HTTP API
                              (SQLite or      (modern DNS policies,
                               Postgres)       legacy static-DNS fallback)

main.go also serves /metrics and /healthz on METRICS_ADDR.
```

## Startup

Order in `main.go`:

1. Load and validate configuration. Invalid values exit non-zero.
2. Create Prometheus metrics and start serving `/metrics` and `/healthz`; `bollard_up` is `0`.
3. Open the state store (`DATABASE_URL`) and run embedded migrations. Failure exits non-zero.
4. Resolve the UniFi site. Fall back to legacy only if the first modern probe returns 404, 405, or 501. Retry transient connection errors, 429, and 5xx with cancelable backoff; bad credentials, invalid certificates, and unknown sites exit non-zero.
5. Connect to the Docker socket. Failure exits non-zero.
6. If `--adopt` was passed, run the adopt phase, then continue into normal operation.
7. Start the event stream and reconcile ticker, set `bollard_up` to `1`, and log the startup banner. If `UNIFI_SKIP_TLS_VERIFY=true` and `UNIFI_CA_CERT` is unset, the banner is followed by a warning that the API key can be captured by anyone who can impersonate the controller.

The first reconcile runs one `RECONCILE_INTERVAL` after startup, not immediately.

Once running, the process exits **non-zero** with `docker event stream error` whenever the Docker event stream ends, including a stream that closes without an error, so the restart policy reconnects. Only a signal-driven shutdown exits 0.

## Event flow

### Container start

1. Docker emits a `start` event; the watcher forwards it with the container's labels.
2. The label parser returns nothing for containers without `dns.bollard/hostname`. Invalid labels are logged as a warning and the container is skipped.
3. A disabled container (`dns.bollard/enabled=false`) is a no-op.
4. The reconciler checks the store and UniFi for the canonical hostname. Any record at that name blocks creation, including disabled records and other types. A failed UniFi listing also blocks creation.
5. The host IP is resolved (see [IP resolution](#ip-resolution)).
6. The UniFi provider creates the A record and returns a record ID.
7. The store persists `{container_id, hostname, ip, record_type, ttl, unifi_record_id}`. If this write fails, the reconciler deletes the record it just created, on a context that survives shutdown cancellation. A failed rollback is logged as `rollback delete of unowned UniFi record failed` and the name stays reserved for the tick.

If the create call itself fails ambiguously (timeout, lost response, 5xx, or no ID returned), the record may exist anyway. The reconciler lists again and claims the record only when exactly one A record matches the hostname and IP and no other store row owns it. A 4xx answer (for example 409) means UniFi refused the create, so nothing is claimed.

### Container stop

1. Docker emits `die` or `destroy`; the watcher reports both as `stop`. (`docker stop` produces `die`.)
2. The store row is looked up by container ID. No row means no-op.
3. The UniFi record is deleted by its stored ID. A 404 from UniFi counts as success. If this fails, the store row is **kept** and the error is logged and returned — the next reconcile tick retries the delete. After the record is gone, the store row is deleted; if that store write fails, orphan cleanup removes the row on the next tick.

### Periodic reconcile

One tick does three things:

1. **Store orphan cleanup.** Store rows whose `unifi_record_id` no longer exists in UniFi are removed from the store (counted in `bollard_reconcile_orphans_cleaned_total`). This reacts to records deleted in UniFi. It does **not** delete UniFi records.
2. **Retry deletes.** Records whose container is no longer running are deleted — a stop delete that failed (the store row was kept) or a missed `die`/`destroy` event — and the store row is removed. A record still referenced by a running row is never deleted.
3. **Missed-event recovery.** Running, enabled, labelled containers with no store row go through the normal start path (counted in `bollard_reconcile_missed_recovered_total`). A failure here is logged and does not fail the tick.

Reconcile deletes UniFi records only for store rows it owns whose container is no longer running — the retry of a failed stop delete and the cleanup of records left behind by a missed event. It never touches records without a store row. See [Operations](operations.md#clean-up-stale-records).

The daemon lists all UniFi records. Disabling an owned record does not drop its ownership row, and records without a row are never deleted by reconcile.

**Empty-list guard.** A list that is empty while the store has rows is treated as suspicious (a wrong `DOCKER_HOST`, a proxy fault, or a daemon that is up before its containers). The first such tick logs a warning and skips the affected pass: orphan cleanup when the UniFi list is empty, retry deletes when the container list is empty. The pass runs if the list is still empty on the next tick. Any non-empty answer resets the count. A genuinely emptied zone is therefore repaired one `RECONCILE_INTERVAL` late.

## Ownership model

Ownership lives in the state store, not in DNS metadata:

```
container_id  →  { hostname, ip, record_type, ttl, unifi_record_id }
```

`container_id` is unique. Deletes use the stored `unifi_record_id` directly, so bollard never deletes UniFi records it did not create or adopt by name.

**Why not TXT ownership records?** They need two records per hostname and DNS queries on every pass. A local store is simpler. The cost: the store is the source of truth, and losing it requires `--adopt`.

### Store backends

`store.NewStore` picks the backend from `DATABASE_URL`: a `postgres://` or `postgresql://` DSN uses Postgres (pgx, pool of 10, embedded migrations in `migrations_pg`); anything else uses SQLite (modernc driver, one connection, embedded migrations). Both are active in current code and share the same schema. Postgres is the only backend that sensibly survives host loss; SQLite needs a persistent volume. Never run two bollard instances against one store or one UniFi site: there is no leader election and no instance tag (see [Roadmap](ROADMAP.md)).

## Adopt phase

`--adopt` runs once before normal operation:

1. List all UniFi records.
2. For each running, enabled, labelled container without a store row, resolve its IP and find records occupying the canonical hostname.
3. If exactly one record occupies the name and is an enabled A record with the same IP, write its ID to the store without modifying UniFi.
4. An unoccupied name can be created. An occupied name that does not meet the adoption criteria stays untouched.

So adopt can create records; it never deletes any. See [Operations](operations.md#adopt-existing-records).

## UniFi client

- Modern API: `/proxy/network/integration/v1/sites/<id>/dns/policies`, paginated. Legacy API: static-DNS entries.
- `UNIFI_SITE` is matched against site name, ID, or `internalReference` (since v0.7.1).
- 15 s request timeout, up to 3 attempts, exponential backoff from 500 ms capped at 10 s with jitter. HTTP 429 retries for any method. 5xx retries for GET and DELETE only. POST is not retried on 5xx, so a failed create waits for the next event or reconcile tick.
- Redirects are not followed.
- API selection lasts for the lifetime of the daemon or plugin provider. Restart after a controller API upgrade to probe again.
- List responses must contain a record list. A missing or `null` list is an error, not an empty zone, so a malformed answer cannot make existing names look free.
- Each paginated listing (sites and records) stops with an error after 100,000 entries or 60 seconds. An error, never a truncated list, so a controller that reports a huge `totalCount` cannot wedge the event loop or make existing names look free.
- A modern or legacy `DELETE` that returns 404 counts as success. HTTP 202 is an error (`accepted but not completed`) because the delete may not have been applied; the ownership row is kept for retry.
- The daemon records one request counter and one latency observation per attempt (see [Monitoring](monitoring.md#unifi-api)). The plugin does not export metrics.

## IP resolution

An `ip-override` label wins. Otherwise the resolver inspects the **host's** network interfaces, once, on first use, and caches the result for the process lifetime:

- ignores down, loopback, link-local, and Docker/virtual interfaces (`docker*`, `br-*`, `veth*`, `virbr*`, `flannel*`, `cni*`, `tunl*`, `weave*`);
- requires **exactly one** remaining IPv4 address. Zero or more than one is an error and the record is not created; set `dns.bollard/ip-override` or fix the interfaces.

It does not inspect the container's own networks. Restart bollard if the host IP changes.

## Shutdown

`SIGTERM` or `SIGINT` cancels the main context. The event loop stops, `bollard_up` returns to `0`, the store and idle UniFi connections are closed, and the process exits `0`. Shutdown never deletes DNS records. A signal during the startup retry loop also exits `0`.


## Failure behaviour

| Scenario | Behaviour |
|---|---|
| UniFi temporarily unreachable at startup | Retry until it recovers or the process receives a stop signal |
| UniFi unreachable during operation | Event/tick errors are logged. A failed delete keeps the ownership row for retry |
| Crash between UniFi create and store write | Orphan in UniFi; `--adopt` reclaims it if the container is still running |
| Store unreachable or corrupt at startup | Process exits non-zero |
| Docker socket unavailable at startup | Process exits non-zero |
| Docker event stream error at runtime | Process returns and exits; restart policy reconnects |
| Container dies while bollard is down | The next reconciliation removes the record if its ownership row still exists |

## Not stateless

The daemon is deliberately stateful and delete-capable. The plugin makes the opposite trade: no store; it creates records and, at shutdown only, deletes the ones it created. Pick by what you operate, not by feature count; the plugin docs explain the plugin's guarantees.
