# bollard roadmap

Every gap and use case carries a priority tag: `P0` (next release), `P1` (near-term),
`P2` (long-term or on-demand). Nothing here is a commitment — priorities shift as
real-world usage reveals what matters.

---

## Current state

Bollard is a Go project with two components that share the `internal/unifi`
client, `internal/dnsname`, `internal/resolver`, and `internal/log` packages.

**Standalone Docker daemon** (`main.go`). Watches Docker events, and for
containers carrying a `dns.bollard/hostname` label creates and deletes A records
on a UniFi controller. A periodic reconciler (default 5m) drops store rows whose
UniFi record vanished and re-registers running labelled containers missing from
the store. `-adopt` matches existing UniFi records to recover the state store.
Record types are A only (CNAME is rejected by the label parser). The daemon
accepts `UNIFI_HOST`, `UNIFI_API_KEY`, `UNIFI_SITE`, `UNIFI_SKIP_TLS_VERIFY`
(default `true`), `UNIFI_CA_CERT`, `DATABASE_URL`, `RECONCILE_INTERVAL`,
`LOG_FORMAT`, `LOG_LEVEL`, and `METRICS_ADDR`; container labels `dns.bollard/{hostname,record-type,ttl,ip-override,enabled}`
shape individual records.

**Traefik provider plugin** (`traefik/`). Polls the Traefik router API, derives
hostnames from `Host()` rules, and creates missing UniFi A records. Create-only,
stateless, no Docker socket. Config keys: `traefikAPI`, `unifiHost`,
`unifiAPIKey`/`unifiAPIKeyFile`, `unifiSite`, `unifiCACert`, `unifiSkipTLSVerify`
(verify-by-default, unlike the daemon), `address`, `pollInterval`.

**UniFi integration.** Modern DNS-policies API (`/proxy/network/integration/v1/...`)
with legacy static-DNS fallback, probed at startup (404/405/501 triggers fallback).
Auth is the `X-API-KEY` header. Retries: 429 always, 5xx for GETs. 404 on delete
is treated as success. Today this is strictly single-controller and single-site:
one host in `Config`, one site resolved at startup.

**State stores.** SQLite (default, `DATABASE_URL=file:...`) or PostgreSQL
(`postgres://`), selected by scheme, identical `records` schema, one embedded
migration each. `container_id` is unique; hostname uniqueness is enforced in
code, not schema. No ownership/instance column — multi-instance safety comes
from reconciler ownership checks and hostname-collision guards (one owner per
hostname during adoption; shared records are never deleted).

**Security posture.** `SECURITY.md` documents key redaction in logs, read-only
Docker socket, and unauthenticated `/metrics` and `/healthz`. DSN passwords are
masked in the startup log. The daemon defaults `UNIFI_SKIP_TLS_VERIFY=true`
(self-signed UniFi deployments); the plugin verifies TLS by default. CI has no
lint, vet, SAST, or image-scan step; dependency security comes from Renovate
vulnerability alerts. Release binaries are checksummed but not signed.

**Observability.** Prometheus metrics on `METRICS_ADDR` (default `:9090`):
record create/delete counts and active gauge, hostname conflicts, reconcile
iterations/duration/orphans/missed-events, Docker event counts, UniFi request
counts/duration and API-version gauge, `bollard_build_info`, `bollard_up`.
`/healthz` plus a `-healthcheck` binary mode used by the Dockerfile `HEALTHCHECK`.
Structured logging in `logfmt` (default) or JSON.

**Testing and CI.** Unit tests per package, `httptest`-based UniFi modern/legacy
tests, Postgres tests behind `TEST_POSTGRES_DSN` (CI service container), and two
smoke tests that skip unless `BOLLARD_SMOKE_IMAGE` / a Traefik image is provided
(the Traefik plugin smoke runs in CI against `traefik:v3.7.19`; CI also builds
the daemon image and runs the daemon image smoke). `go test -race ./...` plus
`go build`. No coverage reporting, no Makefile.

**Release.** release-please (Go, `v` tags) produces GitHub releases with
multi-arch binaries (amd64/arm64), `CHECKSUMS.sha256`, and an SBOM; pushes
multi-arch `ghcr.io` images with SLSA provenance and SBOM; cosign keyless-signs
the image. Renovate keeps dependencies and digests current. Current version
0.7.2, with an unreleased batch of fixes: DSN credential masking, build-context
credential exclusion, delete-retry on reconcile, single-owner adoption, idle-
connection release, hostname validation, dependency-name protection, UniFi wire
hardening, startup retry, and serialized plugin writes in a shared runtime.

---

## Gaps & improvements

### Security

- `P0` — Add static analysis to CI: `go vet` plus `golangci-lint` (or staticcheck) and `govulncheck`. No lint or vulnerability-scan step exists in `ci.yml`.
- `P0` — Add a Trivy image scan of the built Docker image to the release pipeline.
- `P0` — Protect the metrics and health endpoints: optional basic auth or token on `/metrics` and `/healthz`, or default `METRICS_ADDR` to a loopback bind with documented exposure via a Traefik router.
- `P1` — Flip the `UNIFI_SKIP_TLS_VERIFY` daemon default to `false`, steering self-signed deployments to `UNIFI_CA_CERT`; document the migration.
- `P1` — Docker secrets support for `UNIFI_API_KEY` (`/run/secrets/...`), so keys need not appear in environment.
- `P1` — Sign release binaries (cosign or minisign); today they carry checksums only.
- `P2` — Define supported versions and response-time commitments in `SECURITY.md` (currently "not yet defined").
- `P2` — API key rotation without restart (pairs with SIGHUP config reload).

### Multiple endpoints, sites, and domains

- `P1` — Multiple UniFi controllers: a pool of providers with per-container routing (label-driven) and per-controller state isolation. Needs a design before implementation.
- `P1` — Per-container site override: `dns.bollard/site` label routing records to different sites on the same controller; `New()` resolves one site at startup today, make it per-call.
- `P1` — Multiple domains / zones: configurable zone suffix (per site or per controller) so hostnames expand to FQDNs in different domains (e.g. `app.office.arpa` vs `app.garage.arpa`); hostname uniqueness scoped per domain.
- `P2` — Cross-endpoint collision policy: same hostname on different domains is fine; same hostname + domain must not be created on two endpoints.
- `P2` — Traefik plugin multi-target: different router sets pointed at different UniFi controllers/sites.

### Record types and naming

- `P0` — CNAME records: `dns.bollard/record-type: CNAME` with a target label. Both modern and legacy UniFi APIs support CNAME; the store schema is forward-compatible.
- `P1` — Multiple hostnames per container: comma-separated `dns.bollard/hostname`; requires relaxing the `container_id UNIQUE` constraint to `(container_id, hostname)` and reconciler changes.
- `P1` — AAAA records (IPv6): `record-type: AAAA` plus IPv6 inference (link-local vs routable); modern API supports `AAAA_RECORD`, legacy behaviour untested.
- `P2` — PTR / reverse DNS: UniFi exposes no PTR API; needs a separate DNS backend or an upstream API addition. Deferred.
- `P2` — SRV and MX records: defer until requested.

### Networking and IP handling

- `P1` — First-class non-host networking: inspect `container.NetworkSettings.Networks`, pick the routable IP for macvlan/bridge setups, prefer an explicit label, warn on ambiguity; `ip-override` becomes a documented override rather than the only path.
- `P1` — Optional hostname inference from `traefik.http.routers.<name>.rule` or Caddy labels (opt-in config), removing the need for a separate bollard label.

### Operational hardening

- `P1` — TTL drain on container stop: set a short TTL, delete after expiry, via a two-phase reconciler state machine; prevents brief resolution failures during restarts.
- `P1` — Watch-only / dry-run mode: log intended create/delete without UniFi API calls, for testing label configurations.
- `P1` — Instance tag: `BOLLARD_INSTANCE_TAG` column in the store so multiple bollard instances on different hosts pointing at the same controller co-exist; ownership checks filter by tag.
- `P2` — Webhook on record change: POST a JSON payload to `WEBHOOK_URL` on create/delete for Ntfy/Slack-style notifications.
- `P2` — SIGHUP config reload for non-credential settings (reconcile interval, log level, metrics addr); requires separating static from dynamic config.

### Testing and CI

- `P1` — End-to-end test: real Docker daemon + simulated UniFi API exercising the full create/reconcile/delete lifecycle; today only the image smoke and plugin smoke exist, and both skip by default outside their workflows.
- `P2` — Coverage reporting: `go test -coverprofile` with a threshold or badge.
- `P2` — Make the daemon image smoke always-on in CI (build the smoke image in the same run instead of relying on `BOLLARD_SMOKE_IMAGE`).

### Observability

- `P2` — Per-site / per-controller metric labels once multi-endpoint support lands.
- `P2` — Example alerting rules (reconcile stuck, UniFi errors, orphans climbing) in `docs/monitoring.md`.

### Docs and process

- `P1` — Fix the stale `plans/README.md` "rejected" note claiming CI does not build the Docker image; CI builds it.
- `P2` — Makefile (or just documented commands) for the common tasks: test, smoke, build, dev compose.

### Extensibility

- `P1` — external-dns webhook provider so external-dns can use bollard's UniFi client layer, replacing third-party UniFi webhooks.
- `P2` — Pi-hole and AdGuard Home backends behind the `DNSProvider` interface for non-UniFi local DNS.
- `P2` — Kubernetes CRD controller (a `BollardDNSRecord` resource synced to UniFi); separate repository, controller-runtime, leader election.

---

## New use cases

- `P1` — Multi-controller homelab: separate UniFi stacks (e.g. main house + remote cabin) each with its own containers, one bollard per host routing to the right controller.
- `P1` — Per-site domains: the same controller serving different zones per site (`office`/`guest`), each with its own hostname namespace.
- `P1` — Kubernetes services on UniFi DNS via external-dns, without a second UniFi client codebase.
- `P1` — Multiple bollard instances on one controller: instance tags let a NAS and a server host share a controller without stepping on each other's records.
- `P2` — Non-UniFi local DNS: Pi-hole/AdGuard backends for segments of the network that do not run UniFi.
- `P2` — IPv6 dual-stack home: AAAA records alongside A, with sane link-local vs routable inference.
- `P2` — Static, non-Docker hostnames: config-file-declared records for fixed devices (printers, gateways) that never appear as containers.
