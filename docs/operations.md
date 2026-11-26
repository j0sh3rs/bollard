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
| `docker event stream error` | The Docker event stream ended while bollard was running (daemon restart, socket loss, or a stream that closed without an error). The process exits non-zero; the restart policy reconnects |
| `hostname ... is reserved` / `reconcile: hostname is reserved` / `adopt: hostname is reserved` | A label asked for a reserved name; the record was not created or adopted. See [SECURITY.md](../SECURITY.md#defaults-to-review) |
| `hostname ... is reserved, not creating it` | Plugin only: a router rule named the UniFi host, the Traefik API host, `localhost` or a Docker host alias; no record was created |
| `UniFi TLS certificate verification is disabled` | `UNIFI_SKIP_TLS_VERIFY=true` with no `UNIFI_CA_CERT`; the API key is exposed to anyone who can impersonate the controller |
| `UniFi list is empty but store has rows` / `container list is empty but store has rows` | A suspicious empty list. Orphan cleanup or deletes are deferred one tick and run if the list is still empty on the next |
| `rollback delete of unowned UniFi record failed` | A record was created but its ownership row could not be saved, and the cleanup delete failed. Delete the named record by hand |
| `recovered UniFi record after a failed create` | A create reported an error but the record exists; it is now owned by the container |
| `orphaned store record, cleaning up` | A store row pointed at a UniFi record that no longer exists; row removed |
| `reconcile failed` | Store or UniFi listing failed this tick |
| `failed to delete unifi record` | UniFi delete failed during a stop; the store row is kept and retried on the next reconcile tick |
| `reconcile: deleted record for stopped container` | A record whose container is gone was deleted automatically (missed stop or failed delete) |
| `metrics server listening` / `metrics listen failed` | The `/metrics` and `/healthz` listener started, or `METRICS_ADDR` could not be bound (the process exits non-zero) |
| `UniFi unavailable; retrying` | A transient startup failure (connection refused or reset, timeout, 429, 5xx). The delay doubles from 500 ms to 30 s and the process keeps retrying |
| `unifi client init error` | A permanent startup failure: bad key, invalid certificate, or unknown site. The process exits non-zero |
| `docker event stream error` | The Docker event stream failed at runtime. The process exits non-zero and the restart policy reconnects |
| `shutting down` | SIGTERM or SIGINT received. The process exits 0 |

While the controller is merely unreachable, bollard logs `UniFi unavailable; retrying` and keeps trying until it recovers or you stop it. `bollard_up` stays `0` during that time even though `/healthz` returns 200. Configuration, site, authentication, and certificate errors exit non-zero; read the last log line before the exit. See [UniFi error messages](#unifi-error-messages).

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

## Stop and restart

bollard handles `SIGTERM` (`docker stop`) and `SIGINT`, stops the event loop, closes the store and idle UniFi connections, and exits `0`. This also applies while it is still retrying the controller at startup. Stopping never deletes DNS records; records for stopped containers are deleted only when bollard is running to see the stop, or on the next reconcile after it restarts.

Any other exit is non-zero, so `restart: unless-stopped` restarts it. Environment changes need a container recreate (`docker compose up -d bollard`), not a restart.

## UniFi error messages

| Message | Cause | Action |
|---|---|---|
| `unifi: list records: response has no data` | The controller answered a record listing with no record list (`data` missing or `null`). bollard treats that as an error, not as an empty zone, because an empty answer would otherwise look like "nothing exists" and allow duplicate creates. A genuinely empty zone returns an empty list | Check the controller version and logs. If an otherwise healthy controller always answers this way, open an issue with the response body, redacting the API key |
| `unifi: delete record <id> accepted but not completed` (also `legacy delete`) | The controller returned HTTP 202 for a delete, which means it was queued, not applied. The ownership row is kept and the delete is retried on the next reconcile | Usually clears by itself. If it repeats, check in UniFi whether the record is still present |
| `unifi: site probe failed (status=N)` | The first request to the controller failed. 401/403 means a bad or under-privileged key | See [Getting started](getting-started.md#troubleshooting) |
| `unifi: create returned no record ID` | The controller accepted a create but did not report an ID, so bollard cannot track it | Check UniFi for the record and remove it if it is a stray duplicate |

## Verify a release

Release images are built for `linux/amd64` and `linux/arm64`, carry provenance and an SBOM, and are signed with keyless Cosign from the release workflow. Release binaries and an SPDX SBOM are attached to each GitHub release with a `CHECKSUMS.sha256` file.

```bash
cosign verify ghcr.io/j0sh3rs/bollard:<tag> \
  --certificate-identity-regexp '^https://github.com/j0sh3rs/bollard/\.github/workflows/release-please\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

sha256sum -c CHECKSUMS.sha256   # after downloading the binaries and checksum file
```

The running version appears in the `bollard starting` log line and in `bollard_build_info`. Builds that include the `-version` flag also print it with `/bollard -version`.

## Verify metrics

```bash
curl -s localhost:9090/metrics | grep -E '^bollard_(up|records_active|reconcile_last_timestamp_seconds)'
```

See [Monitoring](monitoring.md) for metric labels and alerting examples.
