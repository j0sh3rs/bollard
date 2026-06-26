# Operations guide

## Checking bollard status

```bash
docker compose logs bollard
docker compose logs -f bollard    # follow
docker compose logs --tail=50 bollard
```

**Healthy startup output:**

```
level=info msg="bollard starting" version=v0.2.1
level=info msg="connected to docker" server_version=24.0.5
level=info msg="connected to unifi" host=https://unifi.home.arpa site=default
level=info msg="reconcile loop started" interval=5m0s
```

**Healthy operation output (on container start):**

```
level=info msg="container started" container=myapp hostname=myapp.home.arpa
level=info msg="dns record created" hostname=myapp.home.arpa ip=192.168.1.100 record_id=6787a1b2c3d4e5f6
```

**Healthy operation output (on container stop):**

```
level=info msg="container stopped" container=myapp hostname=myapp.home.arpa
level=info msg="dns record deleted" hostname=myapp.home.arpa record_id=6787a1b2c3d4e5f6
```

**Healthy reconcile output (debug level):**

```
level=debug msg="reconcile tick" owned_records=3 running_containers=3
level=debug msg="reconcile complete" created=0 deleted=0 errors=0
```

**Warning: UniFi temporarily unreachable:**

```
level=warn msg="unifi write failed, will retry" hostname=myapp.home.arpa error="connection refused"
```

This is non-fatal. The reconcile loop will retry on the next tick.

## `--adopt` workflow

Use `--adopt` when:
- The bollard database was deleted or corrupted
- You migrated bollard to a new host and DNS records already exist in UniFi
- You want bollard to take ownership of records it did not create (records that happen to match running containers by hostname + IP)

`--adopt` is non-destructive. It never creates or deletes DNS records. It only claims ownership in the local state store.

**Step-by-step:**

1. Confirm the containers you want adopted are running:

```bash
docker ps --filter "label=dns.bollard/hostname"
```

2. Update your compose file to add the `--adopt` command:

```yaml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    command: ["--adopt"]
    restart: "no"          # prevent restart loop
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    env_file: .env
```

3. Run adopt:

```bash
docker compose up bollard
```

4. Watch the output. Successful adoption looks like:

```
level=info msg="adopt: scanning running containers" count=4
level=info msg="adopt: matched existing record" hostname=myapp.home.arpa record_id=abc123
level=info msg="adopt: matched existing record" hostname=db.home.arpa record_id=def456
level=info msg="adopt: no match found" hostname=newservice.home.arpa
level=info msg="adopt: complete" matched=2 unmatched=1
level=info msg="transitioning to normal operation"
```

Unmatched containers (no corresponding UniFi record) will have records created during normal operation.

5. Remove `command: ["--adopt"]` and `restart: "no"` from the compose file:

```bash
# Edit compose file to restore normal config
docker compose up -d bollard
```

## Backup and restore of state database

The state database records which UniFi DNS record IDs bollard owns. Losing it does not cause data loss in UniFi, but bollard will not be able to clean up its records until you run `--adopt`.

**What to back up:** the SQLite file at the path configured in `DATABASE_URL`.

Default location when using a named volume: find the volume mount point:

```bash
docker volume inspect bollard_bollard-data
# Look for "Mountpoint": "/volume1/@docker/volumes/bollard_bollard-data/_data"
```

**Back up:**

```bash
# Stop bollard first for a clean copy
docker compose stop bollard

sqlite3 /volume1/@docker/volumes/bollard_bollard-data/_data/bollard.db ".backup /backup/bollard-$(date +%Y%m%d).db"

docker compose start bollard
```

Or with `cp` if you accept a brief window of inconsistency (safe while bollard is idle):

```bash
cp /volume1/@docker/volumes/bollard_bollard-data/_data/bollard.db /backup/bollard-$(date +%Y%m%d).db
```

**Restore:**

```bash
docker compose stop bollard
cp /backup/bollard-20260101.db /volume1/@docker/volumes/bollard_bollard-data/_data/bollard.db
docker compose start bollard
```

If the backup is stale (some containers were started/stopped after the backup), run with `--adopt` after restoring to reconcile state.

## Upgrading bollard

For patch releases (no schema changes):

```bash
docker compose pull bollard
docker compose up -d bollard
docker compose logs bollard    # verify clean startup
```

For minor/major releases, check the [CHANGELOG](../CHANGELOG.md) for migration notes before upgrading.

bollard uses embedded migrations — the database schema is updated automatically on startup if needed.

## Viewing owned records

Query the SQLite database directly to see what bollard owns:

```bash
docker compose stop bollard    # optional, for a consistent read

sqlite3 /volume1/@docker/volumes/bollard_bollard-data/_data/bollard.db \
  "SELECT hostname, ip, record_id, created_at FROM records ORDER BY created_at;"
```

Example output:

```
myapp.home.arpa|192.168.1.100|6787a1b2c3d4e5f6|2026-01-15T10:32:00Z
db.home.arpa|192.168.1.100|8899aabbccddeeff|2026-01-15T10:32:05Z
```

With bollard running, you can also read the file with `.dump`:

```bash
sqlite3 /volume1/@docker/volumes/bollard_bollard-data/_data/bollard.db ".dump records"
```

## Monitoring

### Verify metrics are being scraped

```bash
curl -s localhost:9090/metrics | grep bollard_up
```

Expected output:

```
# HELP bollard_up 1 when bollard is running normally, 0 otherwise.
# TYPE bollard_up gauge
bollard_up 1
```

### Check last reconcile time

```bash
curl -s localhost:9090/metrics | grep bollard_reconcile_last_timestamp
```

The value is a Unix epoch timestamp. To compute seconds since last reconcile:

```bash
last=$(curl -s localhost:9090/metrics | awk '/^bollard_reconcile_last_timestamp_seconds / {print $2}')
echo "seconds since last reconcile: $(($(date +%s) - ${last%.*}))"
```

### Check if bollard is healthy

```bash
curl -sf localhost:9090/healthz && echo "healthy"
```

Returns `healthy` with exit code 0 when bollard is running. Non-zero exit or no output indicates bollard is down or the metrics server is not reachable.

For full alerting rules and Grafana panel specs see [docs/monitoring.md](monitoring.md).

## Manually cleaning up orphaned records

If bollard is offline and you need to remove DNS records it created:

**Option 1 — via UniFi UI:**

1. UniFi Network → Settings → DNS
2. Identify records created by bollard (they match your container hostnames)
3. Delete them manually

**Option 2 — identify via state DB, delete via UniFi UI:**

```bash
sqlite3 /path/to/bollard.db \
  "SELECT hostname, record_id FROM records;"
```

Use the `record_id` values to locate the exact records in UniFi.

**Option 3 — clean state DB after manual UniFi cleanup:**

If you deleted records in UniFi manually and want bollard's state to be clean on next start:

```bash
docker compose stop bollard
sqlite3 /path/to/bollard.db "DELETE FROM records;"
docker compose start bollard
```

bollard will re-create records for running containers on the next event or reconcile tick.
