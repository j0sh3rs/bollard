# Architecture

This page explains how the **standalone Docker daemon** works and why it is built that way. It describes the code on `main`. For the Traefik provider plugin (a separate, stateless runtime that shares the UniFi client but not the daemon's store or reconciler) see [Traefik provider plugin](traefik-plugin.md).

| | Standalone daemon (this page) | Traefik plugin |
|---|---|---|
| Runs as | its own container | inside Traefik |
| Discovers hosts from | Docker events + `dns.bollard/*` labels | Traefik router `Host()` rules |
| Needs Docker socket | yes (read-only) | no |
| State | SQLite or Postgres ownership store | none |
| Deletes records | yes, records it owns | never |

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
2. Create Prometheus metrics; `bollard_up` is `0`.
3. Open the state store (`DATABASE_URL`) and run embedded migrations. Failure exits non-zero.
4. Create the UniFi provider: resolve the site, probe the modern API, fall back to legacy only if the modern endpoint answers 404/405/501. Any other probe failure (bad key, TLS, unreachable, unknown site) exits non-zero. There is no startup retry loop beyond the per-request retries described below; rely on `restart: unless-stopped` to try again.
5. Connect to the Docker socket. Failure exits non-zero.
6. Start the metrics/health HTTP server. It starts before the adopt phase.
7. If `--adopt` was passed, run the adopt phase, then continue into normal operation.
8. Start the event stream and reconcile ticker, set `bollard_up` to `1`, and log the startup banner.

The first reconcile runs one `RECONCILE_INTERVAL` after startup, not immediately.

## Event flow

### Container start

1. Docker emits a `start` event; the watcher forwards it with the container's labels.
2. The label parser returns nothing for containers without `dns.bollard/hostname`. Invalid labels are logged as a warning and the container is skipped.
3. A disabled container (`dns.bollard/enabled=false`) is a no-op.
4. The reconciler checks **its own store** for the same hostname. If another container owns it, it increments `bollard_record_hostname_conflicts_total` and returns an error. It does not look at UniFi, so a record created by hand or by another tool with the same name is not detected; UniFi may end up with two records.
5. The host IP is resolved (see [IP resolution](#ip-resolution)).
6. The UniFi provider creates the A record and returns a record ID.
7. The store persists `{container_id, hostname, ip, record_type, ttl, unifi_record_id}`. If this write fails, the reconciler tries to delete the record it just created.

### Container stop

1. Docker emits `die` or `destroy`; the watcher reports both as `stop`. (`docker stop` produces `die`.)
2. The store row is deleted by container ID. No row means no-op.
3. The UniFi record is deleted by its stored ID. A 404 from UniFi counts as success. Any other failure is logged and returned. The store row is already gone at that point, so a failed delete leaves an orphan in UniFi that the daemon no longer tracks. Delete it by hand or restore the store and run `--adopt`.

### Periodic reconcile

One tick does two things:

1. **Store orphan cleanup.** Store rows whose `unifi_record_id` no longer exists in UniFi are removed from the store (counted in `bollard_reconcile_orphans_cleaned_total`). This reacts to records deleted in UniFi. It does **not** delete UniFi records.
2. **Missed-event recovery.** Running, enabled, labelled containers with no store row go through the normal start path (counted in `bollard_reconcile_missed_recovered_total`). A failure here is logged and does not fail the tick.

Reconcile never deletes a UniFi record, and it does not notice containers that stopped while bollard was down or that exited without a `die` event. Those records stay in UniFi and the store until removed manually. See [Operations](operations.md#clean-up-stale-records).

`ListRecords` returns enabled A records only on `main`, so a record disabled in UniFi looks missing and its store row is dropped on the next tick.

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

1. Lists UniFi A records (enabled only).
2. For each running, enabled, labelled container without a store row, it resolves the IP and looks for a UniFi record with the same hostname **and** IP.
3. Match: the record ID is written to the store. Nothing changes in UniFi.
4. No match: the container goes through the normal start path, which **creates** the record.

So adopt can create records; it never deletes any. See [Operations](operations.md#adopt-existing-records).

## UniFi client

- Modern API: `/proxy/network/integration/v1/sites/<id>/dns/policies`, paginated. Legacy API: static-DNS entries.
- `UNIFI_SITE` is matched against site name or ID. On unreleased `main` it also matches `internalReference`; released versions through v0.7.0 do not.
- 15 s request timeout, up to 3 attempts, exponential backoff from 500 ms capped at 10 s with jitter. HTTP 429 retries for any method. 5xx retries for GET and DELETE only. POST is not retried on 5xx, so a failed create waits for the next event or reconcile tick.
- Redirects are not followed.

## IP resolution

An `ip-override` label wins. Otherwise the resolver inspects the **host's** network interfaces, once, on first use, and caches the result for the process lifetime:

- ignores down, loopback, link-local, and Docker/virtual interfaces (`docker*`, `br-*`, `veth*`, `virbr*`, `flannel*`, `cni*`, `tunl*`, `weave*`);
- requires **exactly one** remaining IPv4 address. Zero or more than one is an error and the record is not created; set `dns.bollard/ip-override` or fix the interfaces.

It does not inspect the container's own networks. Restart bollard if the host IP changes.

## Failure behaviour

| Scenario | Behaviour |
|---|---|
| UniFi unreachable at startup | Process exits non-zero; restart policy retries |
| UniFi unreachable during operation | Event/tick errors logged; no store change on failed create; failed delete after store removal leaves an untracked UniFi record |
| Crash between UniFi create and store write | Orphan in UniFi; `--adopt` reclaims it if the container is still running |
| Store unreachable or corrupt at startup | Process exits non-zero |
| Docker socket unavailable at startup | Process exits non-zero |
| Docker event stream error at runtime | Process returns and exits; restart policy reconnects |
| Container dies while bollard is down | Record remains until removed manually |

## Not stateless

The daemon is deliberately stateful and delete-capable. The plugin makes the opposite trade: no store, create-only. Pick by what you operate, not by feature count; the plugin docs explain the plugin's guarantees.
