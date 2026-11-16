# bollard roadmap

Items are grouped by theme and roughly ordered by priority within each group.
Nothing below the Shipped section is a commitment — priorities shift as real-world usage reveals
what matters.

---

## Shipped

### Standalone Docker daemon (through v0.7.0)

- [x] Docker event-driven A record lifecycle (create/delete)
- [x] Label-driven opt-in (`dns.bollard/hostname`)
- [x] SQLite and PostgreSQL ownership stores (`DATABASE_URL` selects by scheme) with embedded migrations
- [x] Periodic reconcile: drops store rows whose UniFi record vanished, registers running labelled containers missing from the store
- [x] `--adopt` for state-store recovery (matches existing records; creates missing ones)
- [x] UniFi modern API (DNS policies) with legacy static-DNS fallback, detected at startup
- [x] Request retries with exponential backoff (429 always; 5xx for GET/DELETE)
- [x] Prometheus metrics and `/healthz`. Note: the three `bollard_unifi_*` metrics are declared but not yet recorded (see [Monitoring](monitoring.md))
- [x] Structured logging (`logfmt`-style text or JSON)
- [x] Multi-arch image (amd64/arm64) with SBOM and Cosign signing, SLSA provenance
- [x] Renovate and release-please

### Traefik provider plugin

- [x] **v0.7.0**: plugin that discovers Traefik router `Host()` names and creates missing UniFi A records. Create-only, stateless, no Docker socket. See [Traefik provider plugin](traefik-plugin.md).

### Unreleased on `main` (not in v0.7.0)

- [ ] Site lookup by `internalReference` (affects the daemon and plugin)
- [ ] Plugin treats every named UniFi record, of any type or enabled state, as occupying a hostname
- [ ] Additional reconcile/concurrency tests

Concurrent plugin instances remain unguarded; run one writer per site and domain scope.

---

## Phase 1 — Record type completeness

### CNAME records

Support `dns.bollard/record-type: CNAME` with a `dns.bollard/cname-target`
label. Both modern and legacy UniFi APIs support CNAME. State store schema is
already forward-compatible (`record_type TEXT`).

**Effort:** Low — extend label parser, provider Create/List; migration adds no
columns.

### Multiple hostnames per container

Allow a single container to register more than one hostname:

```yaml
labels:
  dns.bollard/hostname: "app.home.arpa,app-v2.home.arpa"
```

Requires the store to hold multiple rows per container ID. Current schema uses
`container_id UNIQUE` — requires a migration to relax this constraint and key
on `(container_id, hostname)` instead.

**Effort:** Medium — schema migration + reconciler logic change.

### AAAA records (IPv6)

Schema is forward-compatible. Requires:
- `dns.bollard/record-type: AAAA`
- IPv6 inference (link-local vs routable selection logic)
- UniFi modern API supports `AAAA_RECORD`; legacy endpoint behaviour untested

**Effort:** Low for the A→AAAA plumbing; medium for IPv6 inference reliability.

### PTR / reverse DNS

Register reverse-DNS PTR records alongside A/AAAA. UniFi does not expose PTR
management via its API — would require a separate DNS backend (unbound,
dnsmasq) or a future UniFi API addition.

**Effort:** High, blocked on UniFi API support. Deferred.

---

## Phase 2 — Deployment flexibility

### Non-host-networking support (first-class)

Currently `ip-override` is an escape hatch. Make macvlan and bridge networks
first-class by detecting the container IP from its network configuration:

```
host networking     → use host IP (current behaviour)
macvlan / bridge    → inspect container Networks, pick routable IP
multiple candidates → prefer explicit label, warn if ambiguous
```

Requires inspecting `container.NetworkSettings.Networks` on each start event.

**Effort:** Medium — Docker inspect call is available; heuristic for "routable"
needs testing against real macvlan setups.

### Traefik / Caddy label inference

Optionally read `traefik.http.routers.<name>.rule` or Caddy labels and derive
the hostname automatically, without a separate bollard label. Opt-in via config
(`INFER_FROM_TRAEFIK=true`).

**Effort:** Medium — label parsing extension, no UniFi or store changes.

### SRV and MX records

Low demand for homelab use. Post-CNAME. Defer unless requested.

---

## Phase 3 — Multi-site and multi-controller

### Multiple UniFi sites

Support a per-container `dns.bollard/site` label override, routing records to
different sites on the same controller.

**Effort:** Low — `New()` currently resolves one site ID at startup; make it a
per-call parameter.

### Multiple UniFi controllers

Route different containers to different controllers based on a label or network
namespace. Requires a pool of `DNSProvider` instances and a routing layer.

**Effort:** High — config, routing, and state isolation all need redesign.
Worth a dedicated spec before implementing.

### Pi-hole / AdGuard Home backend

Implement the `DNSProvider` interface against Pi-hole's API and/or AdGuard's
`/control/rewrite/add` endpoint. Allows bollard to manage local DNS on
non-UniFi networks.

**Effort:** Medium per backend — the interface is clean and well-isolated.

---

## Phase 4 — Operational hardening

### Graceful TTL drain on container stop

Instead of immediate deletion, set TTL to a short value (e.g. 30s) on stop
and delete after expiry. Prevents brief resolution failures during container
restarts. Requires a two-phase state machine in the reconciler.

**Effort:** Medium.

### Watch-only / dry-run mode

Log what would be created/deleted without making any UniFi API calls. Useful
for testing label configurations before deploying bollard for real.

**Effort:** Low — wrap the UniFi provider with a no-op implementation.

### Record tagging / multi-instance namespacing

Add a configurable `BOLLARD_INSTANCE_TAG` env var (e.g. `nas1`) stored in the
state DB. Allows multiple bollard instances on different hosts pointing at the
same controller to co-exist without conflicting on the same hostname namespace.

**Effort:** Low — store schema column addition; ownership check filters by tag.

### Docker secrets support for credentials

Read `UNIFI_API_KEY` from `/run/secrets/unifi_api_key` as an alternative to
env vars, following the Docker secrets convention.

**Effort:** Low.

### Webhook on record change

POST a JSON payload to a configurable `WEBHOOK_URL` on create/delete. Enables
integration with notification systems (Ntfy, Slack, etc.) without bollard
needing to know about them directly.

**Effort:** Low.

### Config reload without restart

Support `SIGHUP`-driven reload of non-credential configuration (reconcile
interval, log level, metrics addr). Currently requires a container restart.

**Effort:** Medium — requires separating static config (credentials) from
dynamic config and wiring reload into the running goroutines.

---

## Phase 5 — Kubernetes and beyond

### external-dns webhook provider

Implement the external-dns webhook protocol so bollard can serve as a UniFi
provider for external-dns, replacing `kashalls/external-dns-unifi-webhook`.
Would share the UniFi client layer already in bollard.

**Effort:** Medium — protocol is simple HTTP; UniFi client is already ported.

### Kubernetes controller (CRD-based)

A separate binary (or mode) that watches `BollardDNSRecord` CRDs and syncs to
UniFi, for setups where Kubernetes and Docker run on the same network.

**Effort:** High — separate project scope, controller-runtime dependency, CRD
schema, leader election. Likely a separate repository.

---

## Known gaps

- **Reconcile does not delete UniFi records.** Records for containers that stopped while bollard was down, or whose `die` event was lost, stay until removed by hand. A reconcile pass that deletes owned records whose container is gone is the natural fix.
- **Duplicate-hostname detection is store-only.** The daemon does not check UniFi for an existing record of the same name before creating.
- **UniFi metrics not wired.** `bollard_unifi_requests_total`, `bollard_unifi_request_duration_seconds`, and `bollard_unifi_api_version` are declared but never recorded; `bollard_up` is never reset to `0`.
- **Startup is fail-fast.** An unreachable UniFi controller exits the process rather than retrying in-process.
- **Healthcheck URL.** `-healthcheck` breaks for `METRICS_ADDR` values with a host part.
- **Label validation.** Hostnames are not syntax-checked.
- **Default TLS verification is off** (`UNIFI_SKIP_TLS_VERIFY=true`). Flipping the default is a breaking change to weigh.

## Open questions

- **Duplicate hostname policy:** currently hard-errors on store conflicts. Configurable (`error` / `overwrite` / `first-wins`)?
- **Record TTL drain on stop:** immediate delete vs TTL drain.
- **Auth hardening:** API key via env var only; Docker secrets support is listed above.
