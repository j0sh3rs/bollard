# Operations guide

Task-oriented procedures for the **standalone Docker daemon**. Setup is in [Getting started](getting-started.md); settings in [Configuration](configuration.md); behaviour in [Architecture](architecture.md). The Traefik plugin keeps no state, so none of this applies to it; see [Traefik provider plugin](traefik-plugin.md).

## Check status

```bash
docker compose logs --tail=50 bollard
docker compose ps                       # image healthcheck state
curl -sf localhost:9090/healthz && echo healthy
```

`/healthz` returns 200 whenever the process is serving; it does not check UniFi or Docker connectivity. Use metrics for that ([Monitoring](monitoring.md)).

Log messages you should recognise (abridged):

| Message | Meaning |
|---|---|
| `bollard started` | Event loop running; first reconcile is one interval away |
| `created DNS record` / `deleted DNS record` | Normal lifecycle |
| `invalid labels on container` | Container skipped; fix labels |
| `handle event failed` | A start/stop failed (UniFi error, hostname conflict, ambiguous host IP) |
| `orphaned store record, cleaning up` | A store row pointed at a UniFi record that no longer exists; row removed |
| `reconcile failed` | Store or UniFi listing failed this tick |
| `failed to delete unifi record` | UniFi delete failed during a stop; the store row is kept and retried on the next reconcile tick |
| `reconcile: deleted record for stopped container` | A record whose container is gone was deleted automatically (missed stop or failed delete) |

If the controller is temporarily unavailable, bollard retries with backoff. `bollard_up` remains `0` during startup even if `/healthz` returns 200. Configuration, site, and authentication errors exit non-zero; read the last log line before the exit.

## Adopt existing records

Use `--adopt` when the state store was lost or replaced and UniFi already holds the records, or when you moved bollard to a new host.

For each running, enabled, labelled container without a store row, bollard adopts only a sole enabled A record with the same hostname and IP. It creates a record if the name is free, and skips anything else. It never deletes during adoption, and continues into normal operation when the phase finishes.

If two running containers carry the same `dns.bollard/hostname` label, only the first one processed gets a store row; the rest are rejected as hostname conflicts and logged (`adopt: hostname already owned, skipping` or a failed record creation). Fix the duplicate label — bollard enforces one container per hostname (see [Architecture](architecture.md)).

1. Confirm the containers are running: `docker ps --filter label=dns.bollard/hostname`.
2. Start bollard once with the flag:

```yaml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    command: ["--adopt"]
    # remaining settings unchanged
```

3. `docker compose up -d bollard` and watch for `starting adopt phase` and `adopt phase complete, starting normal operation`.
4. Remove `command: ["--adopt"]` and run `docker compose up -d bollard` again. Leaving it in place is harmless but re-runs adopt on every start.

Adopt does not touch records for containers that are no longer running; see the next section.

## Clean up stale records

bollard handles the common cases automatically: if the store still has a row for the container (stopped while bollard was down, lost `die` event, or a failed stop delete), the next reconcile tick deletes the record and logs `reconcile: deleted record for stopped container`. Manual cleanup remains only for records with **no** store row — for example after the store was reset or lost without running `--adopt`, or records created by other tools.

For the no-row case, list the records and compare against running containers:

```bash
# SQLite: owned records
sqlite3 /path/to/bollard.db "SELECT hostname, ip, unifi_record_id FROM records ORDER BY hostname;"
# Postgres
psql "$DATABASE_URL" -c "SELECT hostname, ip, unifi_record_id FROM records ORDER BY hostname;"

docker ps --filter label=dns.bollard/hostname --format '{{.Names}}'
```

Compare the lists with UniFi DNS settings and a recent store backup. Delete a record only after confirming from the backup or deployment history that you own it and no service uses it. A missing row or Docker label alone does not prove the record is stale.

If bollard is offline and you want records gone, delete them in UniFi; bollard cleans its store when it next runs.

Records for stopped containers with no store row stay in UniFi until deleted. Do not reset the store as a shortcut: with the store empty, bollard no longer knows those records are its own.

## Back up and restore the store

The store maps containers to UniFi record IDs. Losing it does not change UniFi. Restore a backup, or use `--adopt` to recover eligible records for running containers.

**SQLite** (file path from `DATABASE_URL`, usually in a named volume). The image has no shell or `sqlite3`; run these on the host, against the volume's mount point (`docker volume inspect <volume>`):

```bash
docker compose stop bollard
sqlite3 /path/to/_data/bollard.db ".backup /backup/bollard-$(date +%Y%m%d).db"
docker compose start bollard
```

Restore by stopping bollard, copying the backup over the database file, and starting it. If the backup is stale, run [`--adopt`](#adopt-existing-records) afterwards.

**Postgres**:

```bash
pg_dump "$DATABASE_URL" > /backup/bollard-$(date +%Y%m%d).sql
```

Restore into an empty database with `psql`. Migrations run automatically on startup and are idempotent.

## Switch the state store

There is no data copy between backends. To move from SQLite to Postgres (or back):

1. Back up the current store.
2. `docker compose stop bollard`.
3. Set the new `DATABASE_URL`; start with `--adopt` against the new, empty store ([example](examples/postgres-state.yml)).
4. Check `state store ready owned_records=N` matches your running labelled containers.
5. Remove `--adopt`.

Records for containers that are no longer running are not adopted; with no store row, reconcile will not delete them — clean them up as above before switching.

## Upgrade

```bash
docker compose pull bollard
docker compose up -d bollard
docker compose logs --tail=20 bollard
```

Read the [CHANGELOG](../CHANGELOG.md) first for minor and major versions. Embedded migrations run on startup. Pin a version tag in production so upgrades are deliberate; `latest` moves.

## Rotate the UniFi API key

Create a new key in UniFi, update `UNIFI_API_KEY`, run `docker compose up -d bollard` (environment changes need a recreate), then delete the old key.

## Verify metrics

```bash
curl -s localhost:9090/metrics | grep -E '^bollard_(up|records_active|reconcile_last_timestamp_seconds)'
```

See [Monitoring](monitoring.md) for metric labels and alerting examples.
