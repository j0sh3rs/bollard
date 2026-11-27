# bollard roadmap

Every gap and use case carries a priority tag: `P0` (next release), `P1` (near-term),
`P2` (long-term or on-demand). Nothing here is a commitment — priorities shift as
real-world usage reveals what matters. Items that need a design have a proposal in
[`docs/design/`](design/).

---

## Current state

Bollard is a Go project with two components that share the `internal/unifi`
client, `internal/dnsname`, and (daemon only) `internal/resolver` and `internal/log`.

**Standalone Docker daemon** (`main.go`). Watches Docker events, and for
containers carrying a `dns.bollard/hostname` label creates and deletes **A, AAAA
and CNAME** records on a UniFi controller. One label can name several hostnames
(one ownership row and one UniFi record each). The address comes from, in order,
`ip-override`, the `dns.bollard/network` label, the container's only macvlan/ipvlan
network, or the host (IPv4, or a single global/unique-local IPv6 for AAAA).
`INFER_TRAEFIK_HOSTS=true` derives hostnames from Traefik router labels. A periodic
reconciler (default 5m) drops store rows whose UniFi record vanished, retries
deletes for stopped containers, and registers running labelled containers
missing from the store. `-adopt` matches existing UniFi records to recover the
state store. Options: `UNIFI_HOST`, `UNIFI_API_KEY` or `UNIFI_API_KEY_FILE`,
`UNIFI_SITE`, `UNIFI_SKIP_TLS_VERIFY` (default `true`), `UNIFI_CA_CERT`,
`DATABASE_URL`, `BOLLARD_INSTANCE_TAG`, `RECONCILE_INTERVAL`, `STOP_DRAIN`,
`DRY_RUN`, `INFER_TRAEFIK_HOSTS`, `LOG_FORMAT`, `LOG_LEVEL`, and `METRICS_ADDR`
(default `127.0.0.1:9090`). Labels:
`dns.bollard/{hostname,record-type,target,ttl,ip-override,network,enabled}`.

**Traefik provider plugin** (`traefik/`). Polls the Traefik router API, derives
hostnames from `Host()` rules (shared parser in `internal/dnsname`), and creates
missing UniFi A records. Stateless, no Docker socket. At shutdown it deletes only
the records its own instance created. It never creates records for the UniFi
host, the Traefik API host, `localhost`, or Docker host aliases. Config keys:
`traefikAPI`, `unifiHost`, `unifiAPIKey`/`unifiAPIKeyFile`, `unifiSite`,
`unifiCACert`, `unifiSkipTLSVerify` (verify-by-default, unlike the daemon),
`address`, `pollInterval`.

**UniFi integration.** Modern DNS-policies API (`/proxy/network/integration/v1/...`)
with legacy static-DNS fallback, probed at startup (404/405/501 triggers fallback).
Auth is the `X-API-KEY` header. Retries: 429 always, 5xx for GETs. 404 on delete
is treated as success; 202 is not. Listings are bounded (100,000 entries, 60 s).
Both providers implement `RecordUpdater` (used by stop drain). The AAAA, CNAME and
update wire formats follow UniFi's published API reference and a third-party
provider that uses them; they have not been run against every controller version.
Today this is strictly single-controller and single-site: one host in `Config`,
one site resolved at startup (see the [multi-endpoint design](design/multi-endpoint.md)).

**State stores.** SQLite (default, `DATABASE_URL=file:...`) or PostgreSQL
(`postgres://`), selected by scheme, identical `records` schema, two embedded
migrations each. Rows are unique per `(instance_tag, container_id, hostname)` and
carry `target`, `drain_until` and `instance_tag`. Instances with different tags can
share a database. Hostname uniqueness across containers is enforced in code, not
schema, and there is still no cross-instance name arbitration.

**Reliability.** The daemon exits non-zero when the Docker event stream ends,
defers destructive reconcile passes one tick when a list is suspiciously empty,
claims a record committed by an ambiguous failed create (never after a 4xx), and
rolls back an unowned record on a context that survives shutdown. `STOP_DRAIN`
keeps a stopped container's record, with a short TTL, so a restart takes it over
with one update. `DRY_RUN=true` logs creates, updates and deletes against an
in-memory overlay of UniFi and the store.

**Security posture.** `SECURITY.md` documents key redaction in logs, the read-only
Docker socket, and unauthenticated `/metrics` and `/healthz`, which now bind to
loopback by default. API keys can come from a file (Docker secrets). DSN passwords
are masked in the startup log. The daemon still defaults `UNIFI_SKIP_TLS_VERIFY=true`
(self-signed UniFi deployments) and warns at startup; the plugin verifies TLS by
default. CI runs `gofmt`, `go vet`, `golangci-lint`, `govulncheck`, and a Trivy
scan of the daemon image; the release workflow scans the published image again.
Release binaries and the checksum file are signed with keyless Cosign alongside
the signed image.

**Observability.** Prometheus metrics on `METRICS_ADDR`:
record create/delete counts and active gauge, hostname conflicts, reconcile
iterations/duration/orphans/missed-events, Docker event counts, UniFi request
counts/duration and API-version gauge, `bollard_build_info`, `bollard_up`.
`/healthz` plus a `-healthcheck` binary mode used by the Dockerfile `HEALTHCHECK`.
Structured logging in `logfmt` (default) or JSON.

**Testing and CI.** Unit tests per package, `httptest`-based UniFi modern/legacy
tests, Postgres tests behind `TEST_POSTGRES_DSN` (CI service container), the
Traefik plugin smoke (real Traefik against a mock controller, including the
shutdown delete), and an always-on daemon image smoke that runs the built image
against an isolated Docker daemon and a simulated UniFi API through create, stop,
restart, external deletion (reconcile re-creation) and final delete, with a metrics
check. `go test -race ./...` plus `go build`.

**Release.** release-please (Go, `v` tags) produces GitHub releases with
multi-arch binaries (amd64/arm64), `CHECKSUMS.sha256`, an SBOM and Sigstore
bundles; pushes multi-arch `ghcr.io` images with SLSA provenance and SBOM;
cosign keyless-signs the image. Renovate keeps dependencies and digests current.
Current version 0.7.2; 0.8.0 is the unreleased batch.

---

## Gaps & improvements

### Security

- `P1` — Flip the `UNIFI_SKIP_TLS_VERIFY` daemon default to `false`, steering self-signed deployments to `UNIFI_CA_CERT`; document the migration. Held back from 0.8.0 on purpose: it breaks every self-signed deployment that has not set the variable, so it needs its own release note and ideally a deprecation cycle (the startup warning is the first step).
- `P2` — Optional token or basic auth on `/metrics` and `/healthz` for deployments that must scrape across hosts (the loopback default covers the rest).
- `P2` — Define supported versions and response-time commitments in `SECURITY.md` (currently "not yet defined").
- `P2` — API key rotation without restart (pairs with SIGHUP config reload and file re-read).

### Multiple endpoints, sites, and domains

Design: [multi-endpoint](design/multi-endpoint.md).

- `P1` — Multiple UniFi controllers: a pool of providers with per-container routing (label-driven) and per-controller state isolation.
- `P1` — Per-container site override: `dns.bollard/site` label routing records to different sites on the same controller; `New()` resolves one site at startup today, make it per-target.
- `P1` — Multiple domains / zones: configurable zone suffix (per site or per controller) so short hostnames expand to FQDNs in different domains; hostname uniqueness scoped per target.
- `P2` — Cross-endpoint collision policy: same hostname on different domains is fine; same hostname + domain must not be created on two endpoints.
- `P2` — Traefik plugin multi-target: different router sets pointed at different UniFi controllers/sites.

### Record types and naming

- `P1` — TXT records in `internal/unifi` (modern `text` field, legacy `TXT`): prerequisite for the external-dns webhook's default registry.
- `P2` — Both `A` and `AAAA` for one hostname. Today occupancy is by name, so a container publishes one record type per label set; dual-stack needs two records at one name from one container.
- `P2` — PTR / reverse DNS: UniFi exposes no PTR API; needs a separate DNS backend or an upstream API addition. Deferred.
- `P2` — SRV and MX records: defer until requested.

### Operational hardening

- `P1` — Validate AAAA, CNAME and record updates (stop drain) against real controller versions, or add recorded-response contract tests. The wire formats are documented and cross-checked, not yet exercised against a live controller.
- `P2` — Webhook on record change: POST a JSON payload to `WEBHOOK_URL` on create/delete for Ntfy/Slack-style notifications.
- `P2` — SIGHUP config reload for non-credential settings (reconcile interval, log level, metrics addr); requires separating static from dynamic config.
- `P2` — Cross-instance name arbitration (leader election or a claim table) so two instances cannot race on one hostname; instance tags isolate ownership but do not arbitrate names.

### Testing and CI

- `P2` — Coverage reporting: `go test -coverprofile` with a threshold or badge.
- `P2` — Run the daemon smoke against a second simulated controller and a multi-hostname/CNAME container once multi-endpoint lands.

### Observability

- `P2` — Metrics for the 0.8.0 features: drained and taken-over records, dry-run suppressed mutations, per-record-type counts.
- `P2` — Per-site / per-controller metric labels once multi-endpoint support lands.
- `P2` — Example alerting rules (reconcile stuck, UniFi errors, orphans climbing) in `docs/monitoring.md`.

### Docs and process

- `P2` — Makefile (or just documented commands) for the common tasks: test, smoke, build, dev compose. The commands are in `CONTRIBUTING.md` today.

### Extensibility

- `P1` — external-dns webhook provider so external-dns can use bollard's UniFi client layer, replacing third-party UniFi webhooks. Design: [external-dns webhook](design/external-dns-webhook.md); depends on TXT support.
- `P2` — Pi-hole and AdGuard Home backends behind the `DNSProvider` interface for non-UniFi local DNS.
- `P2` — Kubernetes CRD controller (a `BollardDNSRecord` resource synced to UniFi); separate repository, controller-runtime, leader election.

---

## New use cases

- `P1` — Multi-controller homelab: separate UniFi stacks (e.g. main house + remote cabin) each with its own containers, one bollard per host routing to the right controller.
- `P1` — Per-site domains: the same controller serving different zones per site (`office`/`guest`), each with its own hostname namespace.
- `P1` — Kubernetes services on UniFi DNS via external-dns, without a second UniFi client codebase.
- `P2` — Non-UniFi local DNS: Pi-hole/AdGuard backends for segments of the network that do not run UniFi.
- `P2` — Static, non-Docker hostnames: config-file-declared records for fixed devices (printers, gateways) that never appear as containers.

---

## Done in 0.8.0

Kept for one release so the changelog and this file can be read together.

- Security: `go vet`/`golangci-lint`/`govulncheck` in CI; Trivy scan in CI and the release pipeline; loopback `METRICS_ADDR` default; `UNIFI_API_KEY_FILE`; signed release binaries and checksums.
- Records: CNAME and AAAA; several hostnames per container; network-aware address selection; Traefik label inference.
- Operations: stop drain, dry run, instance tag.
- Reliability fixes: stream exit, empty-list guard, create recovery, rollback on shutdown, plugin single-run `Stop` and reserved names.
- Testing: daemon smoke covers restart and reconcile re-creation; Postgres store tests run against a real database in CI.
- Dropped as stale: "always-on daemon image smoke" (already in CI) and the `plans/README.md` note (that directory is not in the repository).
