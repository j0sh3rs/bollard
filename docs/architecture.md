# Architecture

## Component diagram

```
┌───────────────────────────────────────────────────────────┐
│                         bollard                           │
│                                                           │
│  ┌──────────────┐    ┌──────────────┐    ┌─────────────┐ │
│  │    Docker    │    │    Label     │    │  Reconciler │ │
│  │   Watcher   │───►│   Parser     │───►│   Loop      │ │
│  │ (event sub) │    │              │    │  (ticker)   │ │
│  └──────────────┘    └──────┬───────┘    └──────┬──────┘ │
│                             │                   │        │
│                             ▼                   ▼        │
│                    ┌────────────────────────────────┐    │
│                    │        State Store             │    │
│                    │        (SQLite)                │    │
│                    └────────────────┬───────────────┘    │
│                                     │                    │
│                                     ▼                    │
│                    ┌────────────────────────────────┐    │
│                    │       UniFi Provider           │    │
│                    │  (create/delete A records)     │    │
│                    └────────────────────────────────┘    │
└───────────────────────────────────────────────────────────┘
         ▲                                        ▲
   Docker socket                         UniFi HTTP API
```

## Event flow

### Container start event

1. Docker daemon emits a `container start` event
2. **Docker Watcher** receives the event and reads the container's labels
3. **Label Parser** validates labels — hostname syntax, TTL range, enabled flag
4. If `dns.bollard/enabled=false` or no `dns.bollard/hostname` label: no-op
5. **State Store** is checked for an existing record with the same hostname — if found, another container owns it; log an error and skip
6. **UniFi Provider** calls the controller API to create an A record, receiving a record ID
7. **State Store** persists the mapping: `{container_id, hostname, ip, record_id}`

### Container stop event

1. Docker daemon emits a `container stop` (or `die`) event
2. **Docker Watcher** receives the event
3. **State Store** is queried for a record owned by this container ID
4. If no record found: no-op (container had no DNS label or was already cleaned up)
5. **UniFi Provider** calls the controller API to delete the record by ID
6. **State Store** removes the record entry

### Periodic reconcile tick

The reconcile loop runs on the configured `RECONCILE_INTERVAL` (default 5m). It handles containers that exited without emitting a stop event (OOM kill, host crash, Docker daemon restart).

1. **State Store** returns all owned records
2. **Docker Watcher** returns the set of currently running container IDs
3. For each owned record whose container ID is not in the running set:
   a. **UniFi Provider** deletes the record
   b. **State Store** removes the entry
4. For each running container with a `dns.bollard/hostname` label not in the state store:
   a. (This case is handled by the event stream; reconcile only cleans up, does not create)

## Ownership model

bollard tracks ownership via a local SQLite database, not via DNS record metadata. The state store maps:

```
container_id  →  { hostname, ip, unifi_record_id }
```

When a record needs to be deleted, bollard uses the stored `unifi_record_id` to call the UniFi API directly by ID. This avoids querying UniFi for records by name (which would risk modifying records bollard did not create) and makes deletions O(1) regardless of how many DNS records exist.

**Why not TXT records?** Using a TXT record as an ownership marker (common in external-dns style tools) would require bollard to create two records per hostname and to query DNS on every reconcile. The local SQLite store is simpler, more reliable, and works without DNS round-trips. The tradeoff is that the state store is the authoritative source of truth — if it is lost without a backup, `--adopt` is required to recover.

## Adopt phase

When `--adopt` is passed:

1. bollard connects to Docker and UniFi normally
2. It lists all running containers with `dns.bollard/hostname` labels
3. For each container, it queries UniFi for an A record matching the hostname and the resolved IP
4. If found: the record ID is written to the state store as if bollard had created it
5. If not found: the entry is skipped (the record will be created during normal operation)
6. After scanning all containers, bollard transitions to the normal event loop

Adopt never creates or deletes DNS records. It only writes to the local state store.

## Failure recovery guarantees

| Failure scenario | Recovery |
|---|---|
| UniFi unreachable at startup | Exponential backoff retry, no records lost |
| UniFi unreachable during operation | Error logged, state store unchanged, retry on next reconcile |
| bollard crash between UniFi write and state store write | Record exists in UniFi but not in state store; next `--adopt` reclaims it, or delete manually |
| State store corrupted | Fatal startup error; restore from backup or run `--adopt` |
| Docker socket unavailable | Fatal startup error |
| Container exits with no stop event | Reconcile loop detects and cleans up within one interval |

The reconcile loop provides eventual consistency for the common crash/OOM scenarios. The adopt flag handles the uncommon but operationally important case of state store loss.

## IP resolution

When no `dns.bollard/ip-override` label is set, bollard resolves the host IP by inspecting the container's network configuration:

- For `network_mode: host` containers: uses the host machine's primary non-loopback IPv4 address
- For bridge/macvlan containers without override: same host IP (use `ip-override` for containers with their own routable IP)

The resolver reads the host IP once at startup and caches it for the process lifetime. If the NAS IP changes, bollard must be restarted.
