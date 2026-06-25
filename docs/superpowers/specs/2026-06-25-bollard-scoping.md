# bollard — Scoping Document

**Date:** 2026-06-25
**Status:** Draft — pending review

---

## Problem Statement

`external-dns` with the `kashalls/external-dns-unifi-webhook` provider manages DNS
records in a UniFi Network controller for Kubernetes workloads. No equivalent
exists for vanilla Docker (docker compose on a Synology NAS): containers must be
registered and deregistered manually in the UniFi static-DNS UI. bollard closes
this gap.

---

## Goals

- Watch Docker events on a Synology NAS (host-networked containers) and
  automatically create/delete DNS records in a UniFi Network controller.
- Support both UniFi DNS Policies API (`/v2/api/site/default/dns/policies`) and
  legacy static-DNS endpoint (`/proxy/network/v2/api/site/default/static-dns/`),
  auto-detecting which is available.
- Drive record lifecycle entirely from container labels (opt-in).
- Track ownership via a local state store so bollard never clobbers records it
  did not create.
- Self-heal via a periodic reconciliation loop — event-driven updates plus
  interval-based reconcile so stale records are caught after crashes/restarts.
- Provide a `--adopt` startup flag to recover ownership after state-store loss.
- Ship as a single-binary docker compose service; publish releases via
  `release-please`.

---

## Non-Goals (MVP)

- Multi-tenant or public OSS hardening (single operator).
- Support for non-UniFi DNS backends.
- IPv6 / AAAA records (can be added later, schema is forward-compatible).
- Ingress/reverse-proxy label parsing (e.g. Traefik labels) — bollard labels
  are first-class, not derived from another tool's labels.
- HA / leader election (single instance per compose stack).
- Kubernetes support (external-dns already covers that).

---

## Constraints

| Constraint | Detail |
|---|---|
| Runtime | Docker compose on Synology DSM (Container Manager) |
| Networking | Host networking — NAS host IP is the inferred record value |
| Languages | Go (primary), bash (scripts) |
| State store | SQLite (default, zero-config). Interface designed for Postgres from day 1; Postgres backend is post-MVP. |
| Principle of Least Privilege | UniFi credential scoped as narrowly as the API permits |
| Resource footprint | Minimal — single binary, no sidecar services required for SQLite path |

---

## Label Schema

Namespace: `dns.bollard/`

| Label | Required | Default | Notes |
|---|---|---|---|
| `dns.bollard/hostname` | Yes | — | FQDN for the DNS record. Presence of this label opts the container in. |
| `dns.bollard/record-type` | No | `A` | Record type. MVP: `A` only. CNAME, etc. are post-MVP. |
| `dns.bollard/ttl` | No | `300` | TTL in seconds. |
| `dns.bollard/ip-override` | No | NAS host IP | Override inferred IP. Escape hatch for edge cases. |
| `dns.bollard/enabled` | No | `true` | Set to `false` to temporarily suppress record management without removing labels. |

**Inference rule:** if `dns.bollard/ip-override` is absent, bollard reads the
NAS host's primary IP from the Docker daemon info or the host network interface.
If inference is ambiguous (multiple routable IPs), bollard logs a warning and
requires an explicit `dns.bollard/ip-override`.

---

## UniFi API Support

| Mode | Endpoint | Detection |
|---|---|---|
| Modern | `/v2/api/site/default/dns/policies` | Probe on startup; use if 2xx |
| Legacy | `/proxy/network/v2/api/site/default/static-dns/` | Fallback if modern probe fails |

Both modes must support: create record, delete record by ID, list all records.

**Credential scoping:** bollard should use a UniFi local account with the
minimum role that allows DNS record CRUD. Operator must document what that role
is. If UniFi does not offer a DNS-only role, document the minimum role required
and flag this as a security caveat in the README.

---

## Ownership Tracking

UniFi has no TXT-record equivalent. Ownership is tracked entirely in bollard's
state store.

**State store schema (logical):**

```
records
  id               TEXT PRIMARY KEY   -- bollard-internal UUID
  container_id     TEXT NOT NULL      -- Docker container ID
  hostname         TEXT NOT NULL      -- FQDN
  ip               TEXT NOT NULL      -- IP at time of creation
  record_type      TEXT NOT NULL      -- e.g. "A"
  ttl              INTEGER NOT NULL
  unifi_record_id  TEXT NOT NULL      -- ID returned by UniFi API on create
  created_at       TIMESTAMP NOT NULL
  updated_at       TIMESTAMP NOT NULL
```

Bollard only deletes UniFi records whose `unifi_record_id` exists in this table.
It never acts on records it has no ownership entry for.

---

## Reconciliation Model

**Event-driven (real-time):**
- Subscribe to Docker event stream: `start`, `die`, `destroy`.
- On `start`: if container has `dns.bollard/hostname`, create record in UniFi,
  write ownership row to state store.
- On `die`/`destroy`: look up container ID in state store, delete UniFi record,
  remove ownership row.

**Periodic reconcile loop (self-healing):**
- Configurable interval, default `5m`.
- On each tick:
  1. List all running containers with `dns.bollard/hostname`.
  2. List all records in state store.
  3. For records in state store with no matching running container: delete UniFi
     record, remove ownership row (orphan cleanup).
  4. For running containers with label but no state store row: create record,
     write ownership row (missed-event recovery).
- Does not modify records it doesn't own.

---

## Bootstrap / Startup Behavior

| Scenario | Behavior |
|---|---|
| Empty state, clean UniFi | Normal start, reconcile loop handles everything. |
| State has records, UniFi matches | Reconcile confirms, continues. |
| State has records, UniFi missing some | Reconcile recreates missing records. |
| State is empty, UniFi has bollard-era records | **Default:** bollard ignores them — safe, no accidental deletion. Stale records must be cleaned manually or via `--adopt`. |

**`--adopt` flag:**
- Operator-invoked at startup (e.g. `command: ["--adopt"]` in compose).
- Scans running containers with `dns.bollard/hostname`.
- For each, queries UniFi for a record matching hostname + IP.
- If found: writes ownership row to state store and continues.
- If not found: creates record normally.
- Does not delete anything. Non-destructive by design.
- Logs every adoption action.
- After adopt phase completes, bollard transitions directly into normal
  event loop operation — no restart required.

---

## Failure Modes

| Failure | Behavior |
|---|---|
| UniFi unreachable at startup | Bollard starts, logs error, retries with exponential backoff. Does not crash. |
| UniFi write fails during event | Log error, leave state store row absent (so reconcile loop retries). Do not write a dangling ownership row. |
| Container dies ungracefully (no `die` event) | Reconcile loop detects orphaned state row on next tick, deletes UniFi record. |
| State store unavailable | Fatal on startup — bollard cannot safely operate without ownership tracking. |
| Docker socket unavailable | Fatal on startup. |
| Duplicate hostname across two containers | Log error on second container start, do not overwrite record, leave second container unregistered. Document this as unsupported. |

---

## Component List

| Component | Responsibility |
|---|---|
| **Docker watcher** | Subscribes to Docker event stream via Docker SDK. Filters `start`, `die`, `destroy`. Emits typed events to reconciler. |
| **Label parser** | Extracts and validates `dns.bollard/*` labels from container inspect data. Returns typed `RecordSpec` or validation error. |
| **IP resolver** | Infers NAS host IP or returns `ip-override` value. Warns on ambiguity. |
| **State store** | SQLite or Postgres abstraction. CRUD for ownership records. Interface-backed so both backends are swappable. |
| **UniFi client** | Wraps both API versions behind a common `DNSProvider` interface. Auto-detects endpoint on startup. Handles auth (API key or username/password per kashalls' pattern). |
| **Reconciler** | Owns the event loop and periodic tick. Calls label parser, IP resolver, state store, and UniFi client. Single source of truth for create/delete logic. |
| **Adopt command** | One-shot startup mode. Reads running containers, matches against UniFi, writes ownership rows. |
| **Config** | Env-var driven (12-factor). UniFi URL, credentials, reconcile interval, state store DSN, log level. |

---

## What Can Be Lifted from kashalls/external-dns-unifi-webhook

| Asset | Reusability |
|---|---|
| UniFi auth logic (API key + session cookie handling) | High — port directly |
| Modern DNS Policies API client (CRUD) | High — port directly |
| Legacy static-DNS API client | High — port directly |
| Record type definitions / API response structs | High — port directly |
| TXT record ownership logic | Not applicable — bollard uses state store instead |
| external-dns webhook protocol / server | Not applicable |

**Assessment:** the UniFi client layer is the most validated prior art available.
Port it rather than reimplement. Everything above the HTTP layer (event watching,
reconciliation, state store) is net-new.

---

## Hard-to-Reverse Decisions

These must be settled before any code is written:

| Decision | Risk if wrong | Recommendation |
|---|---|---|
| State store interface | Switching backends later requires a migration path. | Define a clean Go interface from day one. Both backends implement it. SQLite is default; Postgres is opt-in via `DATABASE_URL`. |
| Ownership record schema | Adding columns is cheap; removing or renaming is a migration. | Lock the schema above before writing any DB code. Use a migration tool (e.g. `golang-migrate`) from commit 1. |
| `unifi_record_id` as ownership key | If UniFi ever reassigns IDs (e.g. after a controller migration), bollard loses the ability to delete records it owns. | Accept this risk; document it. No better anchor exists given UniFi has no comment field. |
| Label namespace (`dns.bollard/`) | Changing the namespace breaks all existing compose files. | Lock `dns.bollard/` now. |
| `--adopt` is non-destructive by design | If it ever grows a `--force` / delete-unrecognized mode, that's a separate, explicitly named flag. | Document this invariant. Never make `--adopt` destructive. |

---

## Open Questions (Resolved)

1. **UniFi credential role:** Network Admin required — UniFi does not expose a
   DNS-only role. Document as a security caveat in README; operator must create
   a dedicated local account rather than using the primary admin credential.

2. **Reconcile interval:** Configurable via env var, default `5m`.

3. **Release cadence / versioning:** `v0.x.y` pre-1.0 with release-please.
   Confirmed.

4. **Log format:** Both logfmt and JSON supported, configurable via `LOG_FORMAT`
   env var. Default: logfmt (readable in `docker logs`).

5. **Container restart policy interaction:** Unresolved — needs empirical
   verification. Reconcile loop is the safety net regardless; if Docker-managed
   restarts suppress `die`+`start` events, the loop catches drift within one
   interval.

---

## MVP Definition

Bollard is MVP-complete when:

- [ ] Single binary runnable via docker compose on Synology DSM.
- [ ] Detects `dns.bollard/hostname` label on container start/stop.
- [ ] Creates/deletes `A` records in UniFi (both API versions).
- [ ] Infers NAS host IP; respects `dns.bollard/ip-override`.
- [ ] Persists ownership in SQLite.
- [ ] Reconcile loop runs on configurable interval and self-heals.
- [ ] `--adopt` flag recovers ownership after state-store loss.
- [ ] Exponential backoff on UniFi unreachable.
- [ ] All configuration via env vars.
- [ ] Structured logging.
- [ ] README covers: install, label reference, UniFi credential setup, failure modes.
- [ ] Released via release-please with semantic versioning.

## Post-MVP / Later Phases

- Full PostgreSQL state store backend implementation.
- CNAME and other record types.
- Metrics endpoint (Prometheus).
- Multiple hostnames per container (comma-separated or repeated labels).
- Open-source packaging (CONTRIBUTING, issue templates, etc.).

---
