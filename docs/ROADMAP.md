# bollard roadmap

Items are grouped by theme and roughly ordered by priority within each group.
Nothing here is a commitment — priorities shift as real-world usage reveals
what matters.

---

## Shipped (v0.x baseline)

- [x] Docker event-driven A record lifecycle (create/delete)
- [x] Label-driven opt-in (`dns.bollard/hostname`)
- [x] SQLite ownership store with migrations
- [x] Periodic reconcile loop (orphan cleanup + missed-event recovery)
- [x] `--adopt` flag for state-store recovery
- [x] UniFi modern API (DNS Policies) + legacy static-DNS, auto-detected
- [x] Exponential backoff on UniFi failures
- [x] PostgreSQL store backend (`DATABASE_URL=postgres://…`)
- [x] Prometheus metrics + `/healthz` endpoint
- [x] Structured logging (logfmt/json)
- [x] Multi-arch Docker image (amd64/arm64) with SBOM + Cosign signing
- [x] SLSA Build Level 2 provenance
- [x] Renovate with weekly binpacked automerge
- [x] release-please semantic versioning

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

## Open questions

- **Duplicate hostname policy:** currently hard-errors. Should it be
  configurable (`error` / `overwrite` / `first-wins`)?
- **Record TTL drain on stop:** immediate delete vs TTL drain — needs operator
  input on acceptable resolution gap during restarts.
- **Test coverage gaps:** `Reconcile()` and `Adopt()` paths have no unit tests.
  An integration test harness with a mock UniFi HTTP server would close this.
- **Auth hardening:** API key in env var only. Docker secrets support is listed
  above; should it be Phase 1?
